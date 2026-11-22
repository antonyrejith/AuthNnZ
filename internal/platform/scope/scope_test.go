package scope_test

import (
	"context"
	"testing"

	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/ids"
	"github.com/antonyrejith/portun/internal/platform/scope"
)

// TestTenantScopeRequired checks that a missing tenant is an error, never a
// default tenant.
//
// Covers: TEN-01
func TestTenantScopeRequired(t *testing.T) {
	t.Parallel()
	tenant, err := ids.New[ids.TenantKind]()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		ctx  context.Context
		ok   bool
	}{
		{"missing", context.Background(), false},
		{"zero tenant", scope.WithTenant(context.Background(), scope.Tenant{}), false},
		{"set", scope.WithTenant(context.Background(), scope.Tenant{TenantID: tenant}), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := scope.TenantFrom(tt.ctx)
			if tt.ok {
				if err != nil || got.TenantID != tenant {
					t.Fatalf("TenantFrom() = %v, %v", got, err)
				}
				return
			}
			if !errs.Is(err, errs.CodeInternal) {
				t.Fatalf("TenantFrom() error = %v, want internal", err)
			}
		})
	}
}

func TestActorAndRequest(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	if _, ok := scope.ActorFrom(ctx); ok {
		t.Fatal("unexpected actor")
	}
	if _, ok := scope.RequestFrom(ctx); ok {
		t.Fatal("unexpected request")
	}
	ctx = scope.WithActor(ctx, scope.Actor{Type: scope.ActorAdmin, ID: "usr_x"})
	ctx = scope.WithRequest(ctx, scope.Request{RequestID: "req_x", Channel: "api"})
	a, ok := scope.ActorFrom(ctx)
	if !ok || a.Type != scope.ActorAdmin || a.ID != "usr_x" {
		t.Fatalf("ActorFrom() = %+v, %v", a, ok)
	}
	r, ok := scope.RequestFrom(ctx)
	if !ok || r.RequestID != "req_x" || r.Channel != "api" {
		t.Fatalf("RequestFrom() = %+v, %v", r, ok)
	}
}
