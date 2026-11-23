package apimw_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ogen-go/ogen/ogenerrors"
	ogenapi "github.com/ogen-go/ogen/openapi"
	"github.com/ogen-go/ogen/validate"

	"github.com/antonyrejith/portun/internal/api/apimw"
	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/httpx"
)

func paramError(name string, in ogenapi.ParameterLocation, err error) error {
	return &ogenerrors.DecodeParamsError{Err: &ogenerrors.DecodeParamError{Name: name, In: in, Err: err}}
}

func requiredField(name string) error {
	return &validate.Error{Fields: []validate.FieldError{{Name: name, Error: validate.ErrFieldRequired}}}
}

// TestErrorHandler: every error from the generated server becomes a problem
// with the right status, code and JSON pointer, and never echoes what the
// client sent.
//
// Covers: SEC-09
func TestErrorHandler(t *testing.T) {
	t.Parallel()
	nested := &validate.Error{Fields: []validate.FieldError{{
		Name:  "environments",
		Error: &validate.Error{Fields: []validate.FieldError{{Name: "[0]", Error: &validate.Error{Fields: []validate.FieldError{{Name: "kind/x", Error: errors.New("bad enum")}}}}}},
	}}}
	tests := []struct {
		name        string
		err         error
		wantStatus  int
		wantCode    errs.Code
		wantPointer string
		wantField   string // code of the field error
		wantHeader  string // WWW-Authenticate
		notInBody   string
	}{
		{"platform error passes through", errs.New(errs.CodeConflict, "tenant exists"), 409, errs.CodeConflict, "", "", "", ""},
		{"security", &ogenerrors.SecurityError{Err: ogenerrors.ErrSecurityRequirementIsNotSatisfied}, 401, errs.CodeUnauthenticated, "", "", "Bearer", ""},
		{"security from the handler", &ogenerrors.SecurityError{Err: errs.New(errs.CodeUnauthenticated, "no")}, 401, errs.CodeUnauthenticated, "", "", "Bearer", ""},
		{"path pattern", paramError("tenant_id", ogenapi.LocationPath, &validate.NoRegexMatchError{}), 422, errs.CodeValidationFailed, "/path/tenant_id", "invalid_format", "", ""},
		{"query too long", paramError("page_token", ogenapi.LocationQuery, &validate.MaxLengthError{Len: 3000, MaxLength: 2048}), 422, errs.CodeValidationFailed, "/query/page_token", "too_long", "", ""},
		{"header too short", paramError("Change-Reason", ogenapi.LocationHeader, &validate.MinLengthError{MinLength: 1}), 422, errs.CodeValidationFailed, "/headers/Change-Reason", "too_short", "", ""},
		{"required header", paramError("Change-Reason", ogenapi.LocationHeader, requiredField("Change-Reason")), 422, errs.CodeValidationFailed, "/headers/Change-Reason", "required", "", ""},
		{"missing If-Match", paramError("If-Match", ogenapi.LocationHeader, requiredField("If-Match")), 412, errs.CodePreconditionFailed, "", "", "", ""},
		{"cookie", paramError("sid", ogenapi.LocationCookie, errors.New("secret-value")), 422, errs.CodeValidationFailed, "/cookies/sid", "invalid", "", "secret-value"},
		{"params without detail", &ogenerrors.DecodeParamsError{Err: errors.New("secret-value")}, 400, errs.CodeInvalidRequest, "", "", "", "secret-value"},
		{"body validation", &ogenerrors.DecodeRequestError{Err: requiredField("slug")}, 422, errs.CodeValidationFailed, "/slug", "required", "", ""},
		{"nested body validation", &ogenerrors.DecodeRequestError{Err: nested}, 422, errs.CodeValidationFailed, "/environments/0/kind~1x", "invalid", "", "bad enum"},
		{"malformed body", &ogenerrors.DecodeRequestError{Err: &ogenerrors.DecodeBodyError{ContentType: "application/json", Body: []byte("secret-body"), Err: errors.New("syntax")}}, 400, errs.CodeInvalidRequest, "", "", "", "secret-body"},
		{"content type", &ogenerrors.DecodeRequestError{Err: validate.InvalidContentType("text/plain")}, 400, errs.CodeInvalidRequest, "", "", "", ""},
		{"body too large", &ogenerrors.DecodeRequestError{Err: &http.MaxBytesError{Limit: 10}}, 413, errs.CodePayloadTooLarge, "", "", "", ""},
		{"unknown error", errors.New("pq: password authentication failed"), 500, errs.CodeInternal, "", "", "", "password"},
	}
	h := apimw.ErrorHandler(mustOps(t))
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/platform/v1/x", nil)
			h(r.Context(), rec, r, tt.err)
			var p httpx.Problem
			if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
				t.Fatalf("body is not a problem: %v: %s", err, rec.Body)
			}
			if rec.Code != tt.wantStatus || p.Code != tt.wantCode || p.Status != tt.wantStatus ||
				rec.Header().Get("Content-Type") != httpx.ContentTypeProblem {
				t.Fatalf("status %d, problem %+v, content type %q", rec.Code, p, rec.Header().Get("Content-Type"))
			}
			if got := rec.Header().Get("WWW-Authenticate"); got != tt.wantHeader {
				t.Fatalf("WWW-Authenticate = %q", got)
			}
			if tt.wantPointer != "" {
				if len(p.Errors) != 1 || p.Errors[0].Pointer != tt.wantPointer || p.Errors[0].Code != tt.wantField {
					t.Fatalf("errors = %+v, want pointer %s code %s", p.Errors, tt.wantPointer, tt.wantField)
				}
			}
			if tt.notInBody != "" && strings.Contains(rec.Body.String(), tt.notInBody) {
				t.Fatalf("the problem leaks %q: %s", tt.notInBody, rec.Body)
			}
		})
	}
}

// TestNotFoundAndMethodNotAllowed: unmatched requests are problems too, and
// 405 names the allowed methods.
func TestNotFoundAndMethodNotAllowed(t *testing.T) {
	t.Parallel()
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/platform/v1/nope", nil)
	rec := httptest.NewRecorder()
	apimw.NotFound(rec, r)
	if rec.Code != http.StatusNotFound || rec.Header().Get("Content-Type") != httpx.ContentTypeProblem {
		t.Fatalf("not found: %d %v", rec.Code, rec.Header())
	}
	rec = httptest.NewRecorder()
	apimw.MethodNotAllowed(rec, r, "GET, POST")
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "GET, POST" ||
		rec.Header().Get("Content-Type") != httpx.ContentTypeProblem {
		t.Fatalf("method not allowed: %d %v", rec.Code, rec.Header())
	}
}
