package apimw_test

import (
	"strings"
	"testing"

	"github.com/antonyrejith/portun/api/openapi"
	"github.com/antonyrejith/portun/internal/api/apimw"
)

// TestPlatformOperationTable enumerates the Platform API specification and
// checks that every operation declares its permission and audit event, with
// the values the API design requires.
//
// Covers: GOV-11
func TestPlatformOperationTable(t *testing.T) {
	t.Parallel()
	ops, err := apimw.ParseOperations(openapi.Platform)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]struct {
		method, path, permission, event string
		reason                          bool
	}{
		"createPartner":    {"POST", "/platform/v1/partners", "platform.partners.write", "partner.created", false},
		"createTenant":     {"POST", "/platform/v1/tenants", "platform.tenants.write", "tenant.created", false},
		"listTenants":      {"GET", "/platform/v1/tenants", "platform.tenants.read", "none", false},
		"getTenant":        {"GET", "/platform/v1/tenants/{tenant_id}", "platform.tenants.read", "none", false},
		"updateTenant":     {"PATCH", "/platform/v1/tenants/{tenant_id}", "platform.tenants.write", "tenant.updated", false},
		"suspendTenant":    {"POST", "/platform/v1/tenants/{tenant_id}:suspend", "platform.tenants.write", "tenant.suspended", true},
		"resumeTenant":     {"POST", "/platform/v1/tenants/{tenant_id}:resume", "platform.tenants.write", "tenant.resumed", true},
		"deleteTenant":     {"POST", "/platform/v1/tenants/{tenant_id}:delete", "platform.tenants.write", "tenant.deleted", true},
		"exportTenant":     {"POST", "/platform/v1/tenants/{tenant_id}:export", "platform.tenants.export", "tenant.export_requested", false},
		"getOperation":     {"GET", "/platform/v1/operations/{operation_id}", "platform.operations.read", "none", false},
		"listEnvironments": {"GET", "/platform/v1/tenants/{tenant_id}/environments", "platform.tenants.read", "none", false},
		"addHost":          {"POST", "/platform/v1/tenants/{tenant_id}/hosts", "platform.hosts.write", "tenant_host.added", false},
		"removeHost":       {"DELETE", "/platform/v1/hosts/{host}", "platform.hosts.write", "tenant_host.removed", false},
	}
	all := ops.All()
	if len(all) != len(want) {
		t.Fatalf("the specification has %d operations, the test knows %d", len(all), len(want))
	}
	for _, info := range all {
		w, ok := want[info.OperationID]
		if !ok {
			t.Errorf("unexpected operation %q", info.OperationID)
			continue
		}
		if info.Method != w.method || info.PathTemplate != w.path || info.Permission != w.permission ||
			info.AuditEvent != w.event || info.ReasonRequired != w.reason {
			t.Errorf("%s = %+v, want %+v", info.OperationID, info, w)
		}
	}
}

// TestParseOperationsRejects: an operation without operationId, permission or
// audit event, a malformed value, a repeated operationId and an unparsable
// document stop the server from starting.
//
// Covers: GOV-11
func TestParseOperationsRejects(t *testing.T) {
	t.Parallel()
	const head = "openapi: 3.2.0\ninfo: {title: t, version: '1'}\npaths:\n  /x:\n"
	op := func(lines ...string) string {
		return "    get:\n      responses:\n        '200': {description: ok}\n" + strings.Join(lines, "")
	}
	tests := []struct {
		name string
		spec string
		want string // substring of the error, "" for success
	}{
		{"valid", head + op("      operationId: a\n      x-required-permission: a.b\n      x-audit-event: none\n"), ""},
		{"valid with reason", head + op("      operationId: a\n      x-required-permission: a.b\n      x-audit-event: a.changed\n      x-audit-reason: required\n"), ""},
		{"no operationId", head + op("      x-required-permission: a.b\n      x-audit-event: none\n"), "has no operationId"},
		{"no permission", head + op("      operationId: a\n      x-audit-event: none\n"), "has no x-required-permission"},
		{"no audit event", head + op("      operationId: a\n      x-required-permission: a.b\n"), "has no x-audit-event"},
		{"bad permission", head + op("      operationId: a\n      x-required-permission: Admin\n      x-audit-event: none\n"), "not a permission name"},
		{"bad audit event", head + op("      operationId: a\n      x-required-permission: a.b\n      x-audit-event: Changed\n"), "not an event type"},
		{"empty audit event", head + op("      operationId: a\n      x-required-permission: a.b\n      x-audit-event: ''\n"), "non-empty string"},
		{"non-string audit event", head + op("      operationId: a\n      x-required-permission: a.b\n      x-audit-event: [a]\n"), "non-empty string"},
		{"bad reason", head + op("      operationId: a\n      x-required-permission: a.b\n      x-audit-event: none\n      x-audit-reason: optional\n"), "must be \"required\""},
		{"duplicate operationId", head + op("      operationId: a\n      x-required-permission: a.b\n      x-audit-event: none\n") +
			"    post:\n      operationId: a\n      x-required-permission: a.b\n      x-audit-event: a.created\n      responses:\n        '200': {description: ok}\n", "used twice"},
		{"no operations", "openapi: 3.2.0\ninfo: {title: t, version: '1'}\npaths: {}\n", "no operations"},
		{"not a document", "paths: [", "parse the OpenAPI document"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := apimw.ParseOperations([]byte(tt.spec))
			switch {
			case tt.want == "" && err != nil:
				t.Fatalf("error = %v", err)
			case tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)):
				t.Fatalf("error = %v, want it to contain %q", err, tt.want)
			}
		})
	}
	ops, err := apimw.ParseOperations([]byte(head + op("      operationId: a\n      x-required-permission: a.b\n      x-audit-event: none\n")))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ops.Info("missing"); ok {
		t.Fatal("Info found an operation that does not exist")
	}
}
