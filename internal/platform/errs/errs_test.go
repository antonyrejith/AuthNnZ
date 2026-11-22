package errs_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/antonyrejith/portun/internal/platform/errs"
)

func TestCodeTable(t *testing.T) {
	t.Parallel()
	want := map[errs.Code]int{
		errs.CodeInvalidRequest:     http.StatusBadRequest,
		errs.CodeValidationFailed:   http.StatusUnprocessableEntity,
		errs.CodeUnauthenticated:    http.StatusUnauthorized,
		errs.CodePermissionDenied:   http.StatusForbidden,
		errs.CodeNotFound:           http.StatusNotFound,
		errs.CodeMethodNotAllowed:   http.StatusMethodNotAllowed,
		errs.CodeConflict:           http.StatusConflict,
		errs.CodePreconditionFailed: http.StatusPreconditionFailed,
		errs.CodePayloadTooLarge:    http.StatusRequestEntityTooLarge,
		errs.CodeRateLimited:        http.StatusTooManyRequests,
		errs.CodeUnavailable:        http.StatusServiceUnavailable,
		errs.CodeDeadlineExceeded:   http.StatusGatewayTimeout,
		errs.CodeCanceled:           499,
		errs.CodeInternal:           http.StatusInternalServerError,
	}
	if got := len(errs.AllCodes()); got != len(want) {
		t.Fatalf("AllCodes() has %d codes, test covers %d", got, len(want))
	}
	for _, c := range errs.AllCodes() {
		status, ok := want[c]
		if !ok {
			t.Errorf("code %q is not covered by this test", c)
			continue
		}
		if c.HTTPStatus() != status {
			t.Errorf("%s.HTTPStatus() = %d, want %d", c, c.HTTPStatus(), status)
		}
		if c.Title() == "" || !c.Known() {
			t.Errorf("%s has no title or is unknown", c)
		}
	}
	unknown := errs.Code("made_up")
	if unknown.Known() || unknown.HTTPStatus() != http.StatusInternalServerError || unknown.Title() != errs.CodeInternal.Title() {
		t.Error("unknown codes must behave as internal")
	}
}

func TestCodeOf(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want errs.Code
	}{
		{"nil", nil, ""},
		{"direct", errs.New(errs.CodeNotFound, "user not found"), errs.CodeNotFound},
		{"wrapped by fmt", fmt.Errorf("load: %w", errs.New(errs.CodeConflict, "exists")), errs.CodeConflict},
		{"outermost wins", errs.Wrap(errs.CodeUnavailable, errs.New(errs.CodeNotFound, "x"), "y"), errs.CodeUnavailable},
		{"deadline", fmt.Errorf("query: %w", context.DeadlineExceeded), errs.CodeDeadlineExceeded},
		{"canceled", context.Canceled, errs.CodeCanceled},
		{"canceled wrapped", fmt.Errorf("query: %w", context.Canceled), errs.CodeCanceled},
		{"typed nil", error((*errs.Error)(nil)), errs.CodeInternal},
		{"typed nil wrapped", fmt.Errorf("x: %w", (*errs.Error)(nil)), errs.CodeInternal},
		{"plain", errors.New("boom"), errs.CodeInternal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := errs.CodeOf(tt.err); got != tt.want {
				t.Fatalf("CodeOf() = %q, want %q", got, tt.want)
			}
			if tt.err != nil && !errs.Is(tt.err, tt.want) {
				t.Fatalf("Is(%v, %q) = false", tt.err, tt.want)
			}
		})
	}
}

func TestPublicHidesInternalDetails(t *testing.T) {
	t.Parallel()
	cause := errors.New("pq: password authentication failed for user portun_app")
	err := errs.Wrap(errs.CodeUnavailable, cause, "database unavailable")
	code, msg, _ := errs.Public(err)
	if code != errs.CodeUnavailable || msg != "database unavailable" {
		t.Fatalf("Public() = %q, %q", code, msg)
	}
	if !errors.Is(err, cause) {
		t.Fatal("cause must stay reachable with errors.Is")
	}
	if !strings.Contains(err.Error(), "password authentication") {
		t.Fatal("Error() must include the cause for logs")
	}

	code, msg, fields := errs.Public(cause)
	if code != errs.CodeInternal || strings.Contains(msg, "password") || fields != nil {
		t.Fatalf("Public(plain) = %q, %q, %v; must not leak", code, msg, fields)
	}
}

func TestWithFieldsDoesNotMutate(t *testing.T) {
	t.Parallel()
	base := errs.New(errs.CodeValidationFailed, "invalid user")
	a := base.WithFields(errs.FieldError{Pointer: "/email", Code: "format", Detail: "not an email address"})
	b := a.WithFields(errs.FieldError{Pointer: "/name", Code: "required", Detail: "required"})
	if len(base.Fields()) != 0 || len(a.Fields()) != 1 || len(b.Fields()) != 2 {
		t.Fatalf("fields: base %d, a %d, b %d", len(base.Fields()), len(a.Fields()), len(b.Fields()))
	}
	got := a.Fields()
	got[0].Pointer = "/changed"
	if a.Fields()[0].Pointer != "/email" {
		t.Fatal("Fields() must return a copy")
	}
}

// TestTypedNil: a nil *Error stored in an error interface never panics and
// is treated as an internal error (review finding R15).
func TestTypedNil(t *testing.T) {
	t.Parallel()
	var e *errs.Error
	var err error = e
	tests := []struct {
		name string
		fn   func() string
	}{
		{"Error", func() string { return err.Error() }},
		{"Code", func() string { return string(e.Code()) }},
		{"Message", func() string { return e.Message() }},
		{"Fields", func() string { return fmt.Sprint(len(e.Fields())) }},
		{"Unwrap", func() string { return fmt.Sprint(e.Unwrap()) }},
		{"WithFields", func() string { return string(e.WithFields(errs.FieldError{Pointer: "/a"}).Code()) }},
		{"CodeOf", func() string { return string(errs.CodeOf(err)) }},
		{"Public", func() string {
			code, msg, _ := errs.Public(fmt.Errorf("wrap: %w", err))
			return string(code) + " " + msg
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tt.fn() // must not panic
			if tt.name == "CodeOf" || tt.name == "Code" || tt.name == "WithFields" {
				if got != string(errs.CodeInternal) {
					t.Fatalf("%s = %q, want internal", tt.name, got)
				}
			}
			if tt.name == "Public" && got != "internal Internal error" {
				t.Fatalf("Public = %q", got)
			}
		})
	}
}
