package httpx_test

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/httpx"
	"github.com/antonyrejith/portun/internal/platform/ids"
)

type address struct {
	Street string `json:"street"`
	Zip    int    `json:"zip"`
}

type createThing struct {
	Name     string         `json:"name"`
	Count    int            `json:"count"`
	Enabled  bool           `json:"enabled"`
	Address  address        `json:"address"`
	Items    []address      `json:"items"`
	TenantID ids.TenantID   `json:"tenant_id"`
	Tags     []string       `json:"tags,omitempty"`
	Extra    *address       `json:"extra,omitempty"`
	Nothing  map[string]int `json:"nothing,omitempty"`
}

// TestDecodeJSON checks strict decoding and the mapping of every failure to
// a client-safe error with a JSON Pointer.
//
// Covers: SEC-09
func TestDecodeJSON(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		contentType string
		body        string
		limit       int64
		wantCode    errs.Code
		wantFields  []errs.FieldError
	}{
		{name: "valid", contentType: "application/json", body: `{"name":"a","count":2,"address":{"zip":1}}`},
		{name: "valid with charset", contentType: "application/json; charset=UTF-8", body: `{"name":"a"}`},
		{name: "trailing whitespace", contentType: "application/json", body: "{\"name\":\"a\"}\n \t"},
		{name: "missing content type", body: `{}`, wantCode: errs.CodeInvalidRequest},
		{name: "wrong content type", contentType: "text/plain", body: `{}`, wantCode: errs.CodeInvalidRequest},
		{name: "wrong charset", contentType: "application/json; charset=latin1", body: `{}`, wantCode: errs.CodeInvalidRequest},
		{name: "json suffix type", contentType: "application/merge-patch+json", body: `{}`, wantCode: errs.CodeInvalidRequest},
		{name: "empty", contentType: "application/json", body: ``, wantCode: errs.CodeInvalidRequest},
		{name: "whitespace only", contentType: "application/json", body: `   `, wantCode: errs.CodeInvalidRequest},
		{name: "null", contentType: "application/json", body: `null`, wantCode: errs.CodeInvalidRequest},
		{name: "trailing data", contentType: "application/json", body: `{"name":"a"} x`, wantCode: errs.CodeInvalidRequest},
		{name: "second value", contentType: "application/json", body: `{"name":"a"}{}`, wantCode: errs.CodeInvalidRequest},
		{name: "truncated", contentType: "application/json", body: `{"name":`, wantCode: errs.CodeInvalidRequest},
		{
			name: "unknown field", contentType: "application/json", body: `{"name":"a","admin":true}`, wantCode: errs.CodeInvalidRequest,
			wantFields: []errs.FieldError{{Pointer: "/admin", Code: "unknown_field", Detail: "Unknown field."}},
		},
		{
			name: "nested unknown field", contentType: "application/json", body: `{"address":{"city":"x"}}`, wantCode: errs.CodeInvalidRequest,
			wantFields: []errs.FieldError{{Pointer: "/address/city", Code: "unknown_field", Detail: "Unknown field."}},
		},
		{
			name: "case mismatch is unknown", contentType: "application/json", body: `{"Name":"a"}`, wantCode: errs.CodeInvalidRequest,
			wantFields: []errs.FieldError{{Pointer: "/Name", Code: "unknown_field", Detail: "Unknown field."}},
		},
		{
			name: "wrong type", contentType: "application/json", body: `{"count":"two"}`, wantCode: errs.CodeInvalidRequest,
			wantFields: []errs.FieldError{{Pointer: "/count", Code: "invalid_type", Detail: "Must be an integer."}},
		},
		{
			name: "wrong type in array", contentType: "application/json", body: `{"items":[{"zip":1},{"zip":true}]}`, wantCode: errs.CodeInvalidRequest,
			wantFields: []errs.FieldError{{Pointer: "/items/1/zip", Code: "invalid_type", Detail: "Must be an integer."}},
		},
		{
			name: "fraction for integer", contentType: "application/json", body: `{"count":1.5}`, wantCode: errs.CodeInvalidRequest,
			wantFields: []errs.FieldError{{Pointer: "/count", Code: "invalid_type", Detail: "Must be an integer."}},
		},
		{
			name: "boolean wrong type", contentType: "application/json", body: `{"enabled":"yes"}`, wantCode: errs.CodeInvalidRequest,
			wantFields: []errs.FieldError{{Pointer: "/enabled", Code: "invalid_type", Detail: "Must be a boolean."}},
		},
		{
			name: "invalid id value", contentType: "application/json", body: `{"tenant_id":"usr_x"}`, wantCode: errs.CodeInvalidRequest,
			wantFields: []errs.FieldError{{Pointer: "/tenant_id", Code: "invalid_value", Detail: `identifier must start with "ten_"`}},
		},
		{
			name: "duplicate field", contentType: "application/json", body: `{"name":"a","name":"b"}`, wantCode: errs.CodeInvalidRequest,
			wantFields: []errs.FieldError{{Pointer: "/name", Code: "duplicate_field", Detail: "duplicate object member name (at byte offset 12)."}},
		},
		{name: "invalid utf8", contentType: "application/json", body: "{\"name\":\"\xff\"}", wantCode: errs.CodeInvalidRequest},
		{name: "too large declared", contentType: "application/json", body: `{"name":"abcdefghijklmnop"}`, limit: 10, wantCode: errs.CodePayloadTooLarge},
		{name: "too large default", contentType: "application/json", body: `{"name":"` + strings.Repeat("a", 1<<20) + `"}`, wantCode: errs.CodePayloadTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/", strings.NewReader(tt.body))
			if tt.contentType != "" {
				r.Header.Set("Content-Type", tt.contentType)
			}
			var err error
			h := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				var dst createThing
				err = httpx.DecodeJSON(r, &dst)
			})
			if tt.limit > 0 {
				serve(httpx.MaxBytes(tt.limit)(h), r)
				if tt.wantCode == errs.CodePayloadTooLarge && err == nil {
					return // MaxBytes rejected the declared length before the handler
				}
			} else {
				serve(h, r)
			}
			if tt.wantCode == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if got := errs.CodeOf(err); got != tt.wantCode {
				t.Fatalf("code = %q, want %q (err %v)", got, tt.wantCode, err)
			}
			var pe *errs.Error
			if !errors.As(err, &pe) {
				t.Fatalf("not a platform error: %v", err)
			}
			if tt.wantFields != nil {
				if diff := cmp.Diff(tt.wantFields, pe.Fields()); diff != "" {
					t.Fatalf("fields (-want +got):\n%s", diff)
				}
			}
		})
	}
}

func TestDecodeJSONProgrammerErrors(t *testing.T) {
	t.Parallel()
	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/", strings.NewReader(`{}`))
	r.Header.Set("Content-Type", "application/json")
	var dst createThing
	if err := httpx.DecodeJSON(r, dst); errs.CodeOf(err) != errs.CodeInternal {
		t.Fatalf("non-pointer: %v", err)
	}
}

func TestWriteJSON(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		value      any
		wantStatus int
		wantBody   string
		wantCT     string
		wantErr    bool
	}{
		{"object", map[string]string{"user_id": "usr_1"}, http.StatusCreated, "{\"user_id\":\"usr_1\"}\n", "application/json", false},
		{"unencodable", math.Inf(1), http.StatusInternalServerError, "", "application/problem+json", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			err := httpx.WriteJSON(rec, tt.wantStatus, tt.value)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v", err)
			}
			if rec.Code != tt.wantStatus || rec.Header().Get("Content-Type") != tt.wantCT {
				t.Fatalf("status %d, content type %q", rec.Code, rec.Header().Get("Content-Type"))
			}
			if tt.wantBody != "" && rec.Body.String() != tt.wantBody {
				t.Fatalf("body = %q", rec.Body.String())
			}
		})
	}
}
