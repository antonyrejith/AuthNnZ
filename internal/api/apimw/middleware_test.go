package apimw_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ogen-go/ogen/middleware"

	"github.com/antonyrejith/portun/api/openapi"
	"github.com/antonyrejith/portun/internal/api/apimw"
	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/httpx"
	"github.com/antonyrejith/portun/internal/platform/scope"
)

type denyAll struct{}

func (denyAll) Allowed(context.Context, scope.Actor, string) bool { return false }

func mustOps(t *testing.T) apimw.Operations {
	t.Helper()
	ops, err := apimw.ParseOperations(openapi.Platform)
	if err != nil {
		t.Fatal(err)
	}
	return ops
}

func hasPointer(err error, pointer string) bool {
	var e *errs.Error
	if !errors.As(err, &e) {
		return false
	}
	for _, f := range e.Fields() {
		if f.Pointer == pointer {
			return true
		}
	}
	return false
}

// TestMiddleware: authorization, change reason and route label for every
// kind of request the middleware sees.
//
// Covers: GOV-11, SEC-09
func TestMiddleware(t *testing.T) {
	t.Parallel()
	ops := mustOps(t)
	operator := scope.Actor{Type: scope.ActorPlatformOperator, ID: apimw.OperatorActorID}
	long := func(n int) string { return strings.Repeat("a", n) }
	tests := []struct {
		name       string
		operation  string
		actor      *scope.Actor
		authz      apimw.Authorizer
		headers    map[string][]string
		wantCode   errs.Code // "" for success
		wantPtr    string
		wantReason string
		wantTicket string
	}{
		{name: "read needs no reason", operation: "getTenant", actor: &operator},
		{name: "reason recorded", operation: "suspendTenant", actor: &operator, headers: map[string][]string{"Change-Reason": {"  non-payment "}, "Change-Ticket": {"CHG-42"}}, wantReason: "non-payment", wantTicket: "CHG-42"},
		{name: "reason recorded on optional operation", operation: "createTenant", actor: &operator, headers: map[string][]string{"Change-Reason": {"onboarding"}}, wantReason: "onboarding"},
		{name: "required reason missing", operation: "deleteTenant", actor: &operator, wantCode: errs.CodeValidationFailed, wantPtr: "/headers/Change-Reason"},
		{name: "required reason blank", operation: "deleteTenant", actor: &operator, headers: map[string][]string{"Change-Reason": {"   "}}, wantCode: errs.CodeValidationFailed, wantPtr: "/headers/Change-Reason"},
		{name: "reason of 500 characters", operation: "suspendTenant", actor: &operator, headers: map[string][]string{"Change-Reason": {long(500)}}, wantReason: long(500)},
		{name: "reason too long", operation: "suspendTenant", actor: &operator, headers: map[string][]string{"Change-Reason": {long(501)}}, wantCode: errs.CodeValidationFailed, wantPtr: "/headers/Change-Reason"},
		{name: "reason counted in characters", operation: "suspendTenant", actor: &operator, headers: map[string][]string{"Change-Reason": {strings.Repeat("é", 500)}}, wantReason: strings.Repeat("é", 500)},
		{name: "ticket too long", operation: "createTenant", actor: &operator, headers: map[string][]string{"Change-Ticket": {long(101)}}, wantCode: errs.CodeValidationFailed, wantPtr: "/headers/Change-Ticket"},
		{name: "reason repeated", operation: "suspendTenant", actor: &operator, headers: map[string][]string{"Change-Reason": {"a", "b"}}, wantCode: errs.CodeValidationFailed, wantPtr: "/headers/Change-Reason"},
		{name: "reason not UTF-8", operation: "suspendTenant", actor: &operator, headers: map[string][]string{"Change-Reason": {"a\xffb"}}, wantCode: errs.CodeValidationFailed, wantPtr: "/headers/Change-Reason"},
		{name: "reason with control character", operation: "suspendTenant", actor: &operator, headers: map[string][]string{"Change-Reason": {"a\x01b"}}, wantCode: errs.CodeValidationFailed, wantPtr: "/headers/Change-Reason"},
		{name: "no actor", operation: "getTenant", wantCode: errs.CodeUnauthenticated},
		{name: "other actor denied", operation: "getTenant", actor: &scope.Actor{Type: scope.ActorAdmin, ID: "usr_x"}, wantCode: errs.CodePermissionDenied},
		{name: "denied before the reason is checked", operation: "suspendTenant", actor: &operator, authz: denyAll{}, wantCode: errs.CodePermissionDenied},
		{name: "unknown operation", operation: "nope", actor: &operator, wantCode: errs.CodeInternal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			authz := tt.authz
			if authz == nil {
				authz = apimw.OperatorAuthorizer{}
			}
			mw := apimw.Middleware(ops, authz)
			raw := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/x", nil)
			for k, vs := range tt.headers {
				for _, v := range vs {
					raw.Header.Add(k, v)
				}
			}
			ctx := scope.WithRequest(raw.Context(), scope.Request{RequestID: "req_1", Channel: "api", IP: "192.0.2.1"})
			if tt.actor != nil {
				ctx = scope.WithActor(ctx, *tt.actor)
			}
			var got context.Context
			resp, err := mw(middleware.Request{Context: ctx, OperationID: tt.operation, Raw: raw}, func(req middleware.Request) (middleware.Response, error) {
				got = req.Context
				return middleware.Response{Type: "ok"}, nil
			})
			if tt.wantCode != "" {
				if !errs.Is(err, tt.wantCode) || got != nil {
					t.Fatalf("error = %v (handler ran: %v), want %s", err, got != nil, tt.wantCode)
				}
				if tt.wantPtr != "" && !hasPointer(err, tt.wantPtr) {
					t.Fatalf("error %v has no field error at %s", err, tt.wantPtr)
				}
				return
			}
			if err != nil || resp.Type != "ok" || got == nil {
				t.Fatalf("Middleware() = %v, %v", resp, err)
			}
			req, _ := scope.RequestFrom(got)
			if req.ChangeReason != tt.wantReason || req.ChangeTicket != tt.wantTicket || req.Operation != tt.operation ||
				req.RequestID != "req_1" || req.IP != "192.0.2.1" || req.Channel != "api" {
				t.Fatalf("scope.Request = %+v", req)
			}
			if info, ok := apimw.OperationFrom(got); !ok || info.OperationID != tt.operation {
				t.Fatalf("OperationFrom() = %+v, %v", info, ok)
			}
		})
	}
}

// TestMiddlewareSetsRouteLabel: access logs and metrics get the path
// template of the operation, not the mount prefix.
func TestMiddlewareSetsRouteLabel(t *testing.T) {
	t.Parallel()
	ops := mustOps(t)
	mw := apimw.Middleware(ops, apimw.OperatorAuthorizer{})
	rt := httpx.NewRouter()
	rt.HandleFunc("/platform/v1/", func(_ http.ResponseWriter, r *http.Request) {
		ctx := scope.WithActor(r.Context(), scope.Actor{Type: scope.ActorPlatformOperator, ID: "x"})
		_, err := mw(middleware.Request{Context: ctx, OperationID: "getTenant", Raw: r}, func(middleware.Request) (middleware.Response, error) {
			return middleware.Response{}, nil
		})
		if err != nil {
			t.Error(err)
		}
	})
	var route string
	h := httpx.Chain(rt, httpx.RequestID(httpx.RequestIDConfig{}), func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r)
			route = httpx.Route(r)
		})
	})
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/platform/v1/tenants/ten_x", nil))
	if route != "/platform/v1/tenants/{tenant_id}" {
		t.Fatalf("route = %q", route)
	}
}
