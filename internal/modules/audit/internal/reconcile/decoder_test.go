package reconcile

import (
	"testing"

	"github.com/jackc/pglogrepl"
)

func text(vals ...string) *pglogrepl.TupleData {
	t := &pglogrepl.TupleData{}
	for _, v := range vals {
		t.Columns = append(t.Columns, &pglogrepl.TupleDataColumn{DataType: pglogrepl.TupleDataTypeText, Data: []byte(v)})
	}
	return t
}

func rel(id uint32, schema, table string, cols ...string) *pglogrepl.RelationMessage {
	m := &pglogrepl.RelationMessage{RelationID: id, Namespace: schema, RelationName: table}
	for _, c := range cols {
		m.Columns = append(m.Columns, &pglogrepl.RelationMessageColumn{Name: c})
	}
	return m
}

// TestDecoder runs transactions through the decoder.
//
// Covers: GOV-12
func TestDecoder(t *testing.T) {
	t.Parallel()
	keys := func(schema, table string) ([]string, error) {
		switch schema + "." + table {
		case "app.widgets":
			return []string{"tenant_id", "id"}, nil
		case "outbox.messages":
			return []string{"tenant_id", "id"}, nil
		case "app.cache":
			return []string{"k"}, nil
		}
		return nil, nil
	}
	auditRow := func(rows string) *pglogrepl.InsertMessage {
		return &pglogrepl.InsertMessage{RelationID: 2, Tuple: text("t1", "o1", "AUDIT", "audit.t1", `{"change":{"rows":`+rows+`}}`)}
	}
	// widgets columns in table order differ from key order (id before tenant_id).
	relations := []pglogrepl.Message{
		rel(1, "app", "widgets", "id", "tenant_id", "name"),
		rel(2, "outbox", "messages", "tenant_id", "id", "stream", "subject", "payload"),
		rel(3, "app", "cache", "k", "v"),
		rel(4, "app", "nokey", "v"),
	}
	tests := []struct {
		name string
		msgs []pglogrepl.Message
		want []string // table:op of findings
	}{
		{
			name: "declared insert",
			msgs: []pglogrepl.Message{
				&pglogrepl.InsertMessage{RelationID: 1, Tuple: text("w1", "t1", "a")},
				auditRow(`[{"table":"app.widgets","key":["t1","w1"]}]`),
			},
		},
		{
			name: "extra undeclared row in the same transaction",
			msgs: []pglogrepl.Message{
				&pglogrepl.InsertMessage{RelationID: 1, Tuple: text("w1", "t1", "a")},
				&pglogrepl.InsertMessage{RelationID: 1, Tuple: text("w2", "t1", "b")},
				auditRow(`[{"table":"app.widgets","key":["t1","w1"]}]`),
			},
			want: []string{"app.widgets:insert"},
		},
		{
			name: "no audit record",
			msgs: []pglogrepl.Message{
				&pglogrepl.DeleteMessage{RelationID: 1, OldTupleType: 'K', OldTuple: text("w1", "t1", "")},
			},
			want: []string{"app.widgets:delete"},
		},
		{
			name: "key order matters",
			msgs: []pglogrepl.Message{
				&pglogrepl.UpdateMessage{RelationID: 1, NewTuple: text("w1", "t1", "z")},
				auditRow(`[{"table":"app.widgets","key":["w1","t1"]}]`),
			},
			want: []string{"app.widgets:update"},
		},
		{
			name: "key change needs both keys",
			msgs: []pglogrepl.Message{
				&pglogrepl.UpdateMessage{RelationID: 1, OldTupleType: 'K', OldTuple: text("w0", "t1", ""), NewTuple: text("w1", "t1", "z")},
				auditRow(`[{"table":"app.widgets","key":["t1","w1"]}]`),
			},
			want: []string{"app.widgets:update"},
		},
		{
			name: "excluded and bookkeeping tables ignored",
			msgs: []pglogrepl.Message{
				&pglogrepl.InsertMessage{RelationID: 3, Tuple: text("k", "v")},
				&pglogrepl.UpdateMessage{RelationID: 2, NewTuple: text("t1", "o1", "AUDIT", "s", "{}")},
				&pglogrepl.DeleteMessage{RelationID: 2, OldTuple: text("t1", "o1", "", "", "")},
			},
		},
		{
			name: "non-audit outbox insert must be declared",
			msgs: []pglogrepl.Message{
				&pglogrepl.InsertMessage{RelationID: 2, Tuple: text("t1", "o2", "EVENTS", "evt.x.y", "{}")},
			},
			want: []string{"outbox.messages:insert"},
		},
		{
			name: "truncate and keyless rows",
			msgs: []pglogrepl.Message{
				&pglogrepl.TruncateMessage{RelationNum: 1, RelationIDs: []uint32{1}},
				&pglogrepl.InsertMessage{RelationID: 4, Tuple: text("v")},
			},
			want: []string{"app.widgets:truncate", "app.nokey:insert"},
		},
		{
			name: "malformed audit payload",
			msgs: []pglogrepl.Message{
				&pglogrepl.InsertMessage{RelationID: 2, Tuple: text("t1", "o1", "AUDIT", "s", "{")},
			},
			want: []string{"outbox.messages:insert"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			d := newDecoder(Exclusions{"app.cache"}, keys)
			for _, m := range relations {
				if _, _, err := d.handle(m); err != nil {
					t.Fatal(err)
				}
			}
			msgs := append([]pglogrepl.Message{&pglogrepl.BeginMessage{Xid: 42}}, tt.msgs...)
			msgs = append(msgs, &pglogrepl.CommitMessage{})
			var got []string
			for _, m := range msgs {
				u, commit, err := d.handle(m)
				if err != nil {
					t.Fatal(err)
				}
				if commit {
					for _, x := range u {
						if x.XID != 42 {
							t.Errorf("xid = %d", x.XID)
						}
						got = append(got, x.Table+":"+x.Op)
					}
				}
			}
			if len(got) != len(tt.want) {
				t.Fatalf("findings = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("finding %d = %s, want %s", i, got[i], tt.want[i])
				}
			}
		})
	}
}
