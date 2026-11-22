package ocsf_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/antonyrejith/portun/internal/modules/audit"
	"github.com/antonyrejith/portun/internal/modules/audit/internal/ocsf"
	"github.com/antonyrejith/portun/internal/platform/ids"
)

// TestMap checks the class of every record category.
//
// Covers: GOV-01
func TestMap(t *testing.T) {
	t.Parallel()
	tests := []struct {
		category audit.Category
		typ      string
		outcome  audit.Outcome
		class    int
		cat      int
		activity int
	}{
		{audit.CategoryConfiguration, "mfa_policy.updated", audit.OutcomeSuccess, ocsf.ClassEntityManagement, ocsf.CategoryIAM, 3},
		{audit.CategoryConfiguration, "flow.published", audit.OutcomeSuccess, ocsf.ClassEntityManagement, ocsf.CategoryIAM, 3},
		{audit.CategoryData, "user.created", audit.OutcomeSuccess, ocsf.ClassEntityManagement, ocsf.CategoryIAM, 1},
		{audit.CategoryData, "group.deleted", audit.OutcomeSuccess, ocsf.ClassEntityManagement, ocsf.CategoryIAM, 4},
		{audit.CategoryData, "user.locked", audit.OutcomeSuccess, ocsf.ClassEntityManagement, ocsf.CategoryIAM, 9},
		{audit.CategoryAuthentication, "login.succeeded", audit.OutcomeSuccess, ocsf.ClassAuthentication, ocsf.CategoryIAM, 1},
		{audit.CategoryAuthentication, "login.failed", audit.OutcomeFailure, ocsf.ClassAuthentication, ocsf.CategoryIAM, 1},
		{audit.CategoryAuthentication, "logout", audit.OutcomeSuccess, ocsf.ClassAuthentication, ocsf.CategoryIAM, 2},
		{audit.CategoryAuthentication, "token.issued", audit.OutcomeSuccess, ocsf.ClassAuthentication, ocsf.CategoryIAM, 3},
		{audit.CategoryAuthentication, "mfa.challenged", audit.OutcomeSuccess, ocsf.ClassAuthentication, ocsf.CategoryIAM, ocsf.ActivityOther},
		{audit.CategoryAuthorization, "authz.decided", audit.OutcomeSuccess, ocsf.ClassWebResourceAccess, ocsf.CategoryApplication, 1},
		{audit.CategoryAuthorization, "authz.decided", audit.OutcomeFailure, ocsf.ClassWebResourceAccess, ocsf.CategoryApplication, 2},
		{audit.CategorySensitiveRead, "user.pii_revealed", audit.OutcomeSuccess, ocsf.ClassAPIActivity, ocsf.CategoryApplication, 2},
		{audit.CategoryAuditAccess, "audit.searched", audit.OutcomeSuccess, ocsf.ClassDatastoreActivity, ocsf.CategoryApplication, 4},
		{audit.CategoryAuditAccess, "audit.exported", audit.OutcomeSuccess, ocsf.ClassDatastoreActivity, ocsf.CategoryApplication, 1},
		{audit.CategoryKeyManagement, "key.rotated", audit.OutcomeSuccess, ocsf.ClassEntityManagement, ocsf.CategoryIAM, 3},
		{audit.CategoryPlatform, "tenant.created", audit.OutcomeSuccess, ocsf.ClassEntityManagement, ocsf.CategoryIAM, 1},
		{"unknown", "x.y", audit.OutcomeSuccess, ocsf.ClassAPIActivity, ocsf.CategoryApplication, ocsf.ActivityOther},
	}
	for _, tt := range tests {
		t.Run(string(tt.category)+"/"+tt.typ+"/"+string(tt.outcome), func(t *testing.T) {
			t.Parallel()
			c := ocsf.Map(string(tt.category), tt.typ, string(tt.outcome))
			if c.ClassUID != tt.class || c.CategoryUID != tt.cat || c.ActivityID != tt.activity || c.TypeUID != tt.class*100+tt.activity {
				t.Errorf("Map = %+v, want class %d category %d activity %d", c, tt.class, tt.cat, tt.activity)
			}
		})
	}
}

// TestExport converts a record and checks the OCSF attributes.
//
// Covers: GOV-01
func TestExport(t *testing.T) {
	t.Parallel()
	id, err := ids.New[ids.AuditEventKind]()
	if err != nil {
		t.Fatal(err)
	}
	ev := audit.Event{
		ID: id, Sequence: 9, Time: time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC),
		Category: audit.CategoryAuthentication, Type: "login.failed", Outcome: audit.OutcomeFailure, ErrorCode: "unauthenticated",
		Actor: audit.Actor{Type: "user", ID: "usr_1"}, Channel: audit.Channel{Type: "api", IP: "203.0.113.1"},
		Target:  &audit.Target{Type: "user", ID: "usr_1"},
		Request: audit.Request{RequestID: "req_1", TraceID: "abc"},
	}
	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	out, err := ocsf.Export(raw)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"class_uid": 3002.0, "category_uid": 3.0, "activity_id": 1.0, "type_uid": 300201.0, "status_id": 2.0,
		"status_code": "unauthenticated", "time": float64(ev.Time.UnixMilli()),
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
	meta, _ := got["metadata"].(map[string]any)
	if meta["uid"] != id.String() || meta["version"] != ocsf.Version {
		t.Errorf("metadata = %v", meta)
	}
	unmapped, ok := got["unmapped"].(map[string]any)
	if !ok {
		t.Fatal("no unmapped attributes")
	}
	if _, ok := unmapped["portun"]; !ok {
		t.Error("the original record is not carried")
	}
	for _, bad := range []string{`{`, `{"type":"x"}`} {
		if _, err := ocsf.Export([]byte(bad)); err == nil {
			t.Errorf("Export(%s) succeeded", bad)
		}
	}
}
