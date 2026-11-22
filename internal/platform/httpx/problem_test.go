package httpx_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/httpx"
	"github.com/antonyrejith/portun/internal/platform/scope"
)

const secretCause = "pq: password authentication failed for user admin at 10.0.0.7"

// TestWriteProblemEveryCode checks status, type, title, code and detail for
// every error code, and that internal causes never reach the client.
//
// Covers: SEC-09
func TestWriteProblemEveryCode(t *testing.T) {
	t.Parallel()
	for _, code := range errs.AllCodes() {
		t.Run(string(code), func(t *testing.T) {
			t.Parallel()
			logger, logs := newLogger()
			r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/v1/things/x?token=abc", nil)
			r = r.WithContext(scope.WithRequest(r.Context(), scope.Request{RequestID: "req_test"}))
			h := httpx.Recover(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				httpx.WriteProblem(w, r, errs.Wrap(code, errors.New(secretCause), "Client-safe message."))
			}))
			rec := serve(h, r)
			if rec.Code != code.HTTPStatus() {
				t.Fatalf("status = %d, want %d", rec.Code, code.HTTPStatus())
			}
			if ct := rec.Header().Get("Content-Type"); ct != httpx.ContentTypeProblem {
				t.Fatalf("content type = %q", ct)
			}
			if strings.Contains(rec.Body.String(), "password") {
				t.Fatalf("cause leaked: %s", rec.Body.String())
			}
			got := decodeProblem(t, rec.Body)
			want := httpx.Problem{
				Type:      httpx.ProblemTypeBase + string(code),
				Title:     code.Title(),
				Status:    code.HTTPStatus(),
				Code:      code,
				Detail:    "Client-safe message.",
				Instance:  "/v1/things/x",
				RequestID: "req_test",
			}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Fatalf("problem (-want +got):\n%s", diff)
			}
			rec5xx := code.HTTPStatus() >= 500
			logged := logs.find(t, "request failed")
			if rec5xx && (logged["level"] != "ERROR" || !strings.Contains(fmt.Sprint(logged["error"]), "password")) {
				t.Fatalf("5xx must be logged at error with cause: %v", logged)
			}
			if !rec5xx && logged["level"] != "DEBUG" {
				t.Fatalf("4xx must be logged at debug: %v", logged)
			}
		})
	}
}

// TestWriteProblemMapping covers plain errors, unknown codes, field errors
// and size limits.
//
// Covers: SEC-09
func TestWriteProblemMapping(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   errs.Code
		wantDetail string
		wantFields []errs.FieldError
	}{
		{"plain error", errors.New(secretCause), 500, errs.CodeInternal, "Internal error", nil},
		{"nil error", nil, 500, errs.CodeInternal, "internal error", nil},
		{"unknown code", errs.New("made_up", secretCause), 500, errs.CodeInternal, "Internal error", nil},
		{"deadline", fmt.Errorf("query: %w", context.DeadlineExceeded), 504, errs.CodeDeadlineExceeded, "Deadline exceeded", nil},
		{"max bytes", &http.MaxBytesError{Limit: 10}, 413, errs.CodePayloadTooLarge, "Request body must not exceed 10 bytes.", nil},
		{
			"field errors",
			errs.New(errs.CodeValidationFailed, "2 fields are invalid.").WithFields(
				errs.FieldError{Pointer: "/email", Code: "invalid_format", Detail: "Must be an email address."},
				errs.FieldError{Pointer: "/password", Code: "too_short", Detail: "Must be at least 12 characters."},
			),
			422, errs.CodeValidationFailed, "2 fields are invalid.",
			[]errs.FieldError{
				{Pointer: "/email", Code: "invalid_format", Detail: "Must be an email address."},
				{Pointer: "/password", Code: "too_short", Detail: "Must be at least 12 characters."},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/x", nil)
			rec := httptest.NewRecorder()
			httpx.WriteProblem(rec, r, tt.err)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if strings.Contains(rec.Body.String(), "password authentication") {
				t.Fatalf("cause leaked: %s", rec.Body.String())
			}
			p := decodeProblem(t, rec.Body)
			if p.Code != tt.wantCode || p.Detail != tt.wantDetail || p.Status != tt.wantStatus {
				t.Fatalf("problem = %+v", p)
			}
			if diff := cmp.Diff(tt.wantFields, p.Errors); diff != "" {
				t.Fatalf("errors (-want +got):\n%s", diff)
			}
			if rec.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("problems must not be cached")
			}
		})
	}
}
