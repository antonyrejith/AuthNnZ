// Package reconcile is the independent completeness check of ADR-0008
// (GOV-12): a logical-replication reader (pgoutput) follows every row change
// in the module schemas and, for each committed transaction, checks that
// every inserted, updated or deleted row (table and primary key) is declared
// in change.rows of an audit record inserted into the outbox in the same
// transaction. Rows that are not are reported as Unaudited.
//
// Not checked (the reviewed allowlist): the audit schema itself, updates
// and deletes of outbox.messages (relay bookkeeping and the writer's
// deletes), goose version tables (migrations are recorded by the release
// pipeline), and configured ephemeral tables. AUDIT outbox inserts are the
// records themselves. Every other row, including outbox inserts for other
// streams, must be declared.
package reconcile

import (
	"encoding/json"
	"strings"

	"github.com/jackc/pglogrepl"

	"github.com/antonyrejith/portun/internal/platform/errs"
)

// OutboxTable is the transactional outbox (ADR-0007).
const OutboxTable = "outbox.messages"

// AuditStream is the outbox stream of audit records.
const AuditStream = "AUDIT"

// Unaudited is a row change no audit record in its transaction declares.
type Unaudited struct {
	XID    uint32   `json:"xid"` // 32-bit transaction ID (as pgoutput reports it)
	Table  string   `json:"table"`
	Key    []string `json:"key,omitempty"`
	Op     string   `json:"op"` // insert, update, delete, truncate
	Reason string   `json:"reason"`
}

// Exclusions is the reviewed list of tables not reconciled: "schema.table"
// or "schema.*".
type Exclusions []string

func (x Exclusions) excluded(schema, table string) bool {
	if schema == "audit" || table == "goose_db_version" {
		return true
	}
	for _, e := range x {
		if e == schema+".*" || e == schema+"."+table {
			return true
		}
	}
	return false
}

// KeyResolver returns the primary key column names of a table in key order
// (nil when it has none).
type KeyResolver func(schema, table string) ([]string, error)

type relation struct {
	schema, table string
	columns       []string
	keyIdx        []int // column positions of the primary key, in key order
}

type change struct {
	table string
	key   []string
	op    string
	note  string
}

// decoder turns pgoutput messages into per-transaction findings.
type decoder struct {
	exclude   Exclusions
	keys      KeyResolver
	relations map[uint32]*relation
	inTx      bool
	xid       uint32
	changes   []change
	declared  map[string]bool
	problems  []Unaudited // records that could not be read
}

func newDecoder(exclude Exclusions, keys KeyResolver) *decoder {
	return &decoder{exclude: exclude, keys: keys, relations: map[uint32]*relation{}}
}

func rowKey(table string, key []string) string {
	return table + "\x00" + strings.Join(key, "\x00")
}

// handle processes one message; on a commit it returns the transaction's
// unaudited rows (possibly none) and true.
func (d *decoder) handle(m pglogrepl.Message) ([]Unaudited, bool, error) {
	switch m := m.(type) {
	case *pglogrepl.RelationMessage:
		return nil, false, d.relation(m)
	case *pglogrepl.BeginMessage:
		d.inTx, d.xid, d.changes, d.declared, d.problems = true, m.Xid, nil, map[string]bool{}, nil
	case *pglogrepl.InsertMessage:
		d.row(m.RelationID, "insert", m.Tuple, nil)
	case *pglogrepl.UpdateMessage:
		d.row(m.RelationID, "update", m.NewTuple, m.OldTuple)
	case *pglogrepl.DeleteMessage:
		d.row(m.RelationID, "delete", nil, m.OldTuple)
	case *pglogrepl.TruncateMessage:
		for _, id := range m.RelationIDs {
			if r := d.relations[id]; r != nil && !d.exclude.excluded(r.schema, r.table) {
				d.changes = append(d.changes, change{table: r.schema + "." + r.table, op: "truncate", note: "truncate cannot be declared"})
			}
		}
	case *pglogrepl.CommitMessage:
		if !d.inTx {
			return nil, false, nil
		}
		d.inTx = false
		return d.evaluate(), true, nil
	}
	return nil, false, nil
}

func (d *decoder) relation(m *pglogrepl.RelationMessage) error {
	r := &relation{schema: m.Namespace, table: m.RelationName}
	pos := map[string]int{}
	for i, c := range m.Columns {
		r.columns = append(r.columns, c.Name)
		pos[c.Name] = i
	}
	pk, err := d.keys(m.Namespace, m.RelationName)
	if err != nil {
		return errs.Wrap(errs.CodeUnavailable, err, "reconcile: resolve the primary key of "+m.Namespace+"."+m.RelationName)
	}
	for _, name := range pk {
		if i, ok := pos[name]; ok {
			r.keyIdx = append(r.keyIdx, i)
		}
	}
	if len(r.keyIdx) != len(pk) || len(pk) == 0 {
		// Fall back to the replica identity columns in table order.
		r.keyIdx = r.keyIdx[:0]
		for i, c := range m.Columns {
			if c.Flags&1 == 1 {
				r.keyIdx = append(r.keyIdx, i)
			}
		}
	}
	d.relations[m.RelationID] = r
	return nil
}

func tupleKey(r *relation, t *pglogrepl.TupleData) ([]string, bool) {
	if t == nil || len(r.keyIdx) == 0 {
		return nil, false
	}
	key := make([]string, 0, len(r.keyIdx))
	for _, i := range r.keyIdx {
		if i >= len(t.Columns) {
			return nil, false
		}
		c := t.Columns[i]
		switch c.DataType {
		case pglogrepl.TupleDataTypeText, pglogrepl.TupleDataTypeBinary:
			key = append(key, string(c.Data))
		default: // null or unchanged TOAST: not identifiable
			return nil, false
		}
	}
	return key, true
}

func column(r *relation, t *pglogrepl.TupleData, name string) ([]byte, bool) {
	for i, c := range r.columns {
		if c == name && t != nil && i < len(t.Columns) && t.Columns[i].DataType == pglogrepl.TupleDataTypeText {
			return t.Columns[i].Data, true
		}
	}
	return nil, false
}

func (d *decoder) row(relID uint32, op string, newTuple, oldTuple *pglogrepl.TupleData) {
	r := d.relations[relID]
	if r == nil {
		d.changes = append(d.changes, change{table: "?", op: op, note: "change on an unknown relation"})
		return
	}
	table := r.schema + "." + r.table
	if table == OutboxTable {
		if op != "insert" {
			return // relay bookkeeping and writer deletes
		}
		if stream, _ := column(r, newTuple, "stream"); string(stream) == AuditStream {
			d.declare(r, newTuple)
			return
		}
	}
	if d.exclude.excluded(r.schema, r.table) {
		return
	}
	tuple := newTuple
	if op == "delete" {
		tuple = oldTuple
	}
	key, ok := tupleKey(r, tuple)
	if !ok {
		d.changes = append(d.changes, change{table: table, op: op, note: "row has no identifiable primary key"})
		return
	}
	d.changes = append(d.changes, change{table: table, key: key, op: op})
	if op == "update" && oldTuple != nil {
		if oldKey, ok := tupleKey(r, oldTuple); ok && rowKey(table, oldKey) != rowKey(table, key) {
			d.changes = append(d.changes, change{table: table, key: oldKey, op: op})
		}
	}
}

// auditPayload is the part of an outbox audit record the reconciler reads.
type auditPayload struct {
	Change *struct {
		Rows []struct {
			Table string   `json:"table"`
			Key   []string `json:"key"`
		} `json:"rows"`
	} `json:"change"`
}

func (d *decoder) declare(r *relation, t *pglogrepl.TupleData) {
	payload, ok := column(r, t, "payload")
	if !ok {
		d.problems = append(d.problems, Unaudited{XID: d.xid, Table: OutboxTable, Op: "insert", Reason: "audit record payload not readable"})
		return
	}
	var p auditPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		d.problems = append(d.problems, Unaudited{XID: d.xid, Table: OutboxTable, Op: "insert", Reason: "audit record payload is not valid JSON"})
		return
	}
	if p.Change == nil {
		return
	}
	for _, row := range p.Change.Rows {
		d.declared[rowKey(row.Table, row.Key)] = true
	}
}

func (d *decoder) evaluate() []Unaudited {
	out := d.problems
	for _, c := range d.changes {
		if c.note == "" && d.declared[rowKey(c.table, c.key)] {
			continue
		}
		reason := c.note
		if reason == "" {
			reason = "no audit record in the transaction declares this row"
		}
		out = append(out, Unaudited{XID: d.xid, Table: c.table, Key: c.key, Op: c.op, Reason: reason})
	}
	d.changes, d.declared, d.problems = nil, nil, nil
	return out
}
