package idempotency

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/antonyrejith/portun/internal/platform/errs"
)

func TestValidKey(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		values []string
		ok     bool
	}{
		{"simple", []string{"abc-123"}, true},
		{"printable ASCII", []string{"a b!~{}\"'"}, true},
		{"255 characters", []string{strings.Repeat("k", 255)}, true},
		{"256 characters", []string{strings.Repeat("k", 256)}, false},
		{"empty", []string{""}, false},
		{"two headers", []string{"a", "b"}, false},
		{"tab", []string{"a\tb"}, false},
		{"newline", []string{"a\nb"}, false},
		{"DEL", []string{"a\x7fb"}, false},
		{"non-ASCII", []string{"café"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			key, err := validKey(tt.values)
			if tt.ok {
				if err != nil || key != tt.values[0] {
					t.Fatalf("validKey() = %q, %v", key, err)
				}
				return
			}
			if !errs.Is(err, errs.CodeValidationFailed) {
				t.Fatalf("validKey() error = %v", err)
			}
			if f := err.(*errs.Error).Fields(); len(f) != 1 || f[0].Pointer != "/headers/Idempotency-Key" { //nolint:errorlint,errcheck // validKey returns *errs.Error
				t.Fatalf("field errors = %+v", f)
			}
		})
	}
}

func TestRequestHash(t *testing.T) {
	t.Parallel()
	req := func(method, target, body string) string {
		r := httptest.NewRequestWithContext(context.Background(), method, target, strings.NewReader(body))
		return requestHash(r, []byte(body))
	}
	base := req(http.MethodPost, "/a/b?x=1", "body")
	if base != req(http.MethodPost, "/a/b?x=1", "body") {
		t.Fatal("the hash is not deterministic")
	}
	for name, other := range map[string]string{
		"method": req(http.MethodPut, "/a/b?x=1", "body"),
		"path":   req(http.MethodPost, "/a/c?x=1", "body"),
		"query":  req(http.MethodPost, "/a/b?x=2", "body"),
		"body":   req(http.MethodPost, "/a/b?x=1", "Body"),
		// Parts must not run together: moving a byte from the path to the body.
		"boundary": req(http.MethodPost, "/a/", "bbody"),
	} {
		if other == base {
			t.Errorf("a different %s has the same hash", name)
		}
	}
}

func TestNewRequiresDB(t *testing.T) {
	t.Parallel()
	if _, err := New(Config{}); !errs.Is(err, errs.CodeInternal) {
		t.Fatalf("New() error = %v", err)
	}
}

func TestCaptureWriter(t *testing.T) {
	t.Parallel()
	t.Run("status headers and body", func(t *testing.T) {
		t.Parallel()
		rec := httptest.NewRecorder()
		cw := &captureWriter{ResponseWriter: rec, limit: 10}
		cw.Header().Set("X-Thing", "1")
		cw.Header().Set("X-Request-Id", "req_1")
		cw.WriteHeader(http.StatusCreated)
		if _, err := cw.Write([]byte("hello")); err != nil {
			t.Fatal(err)
		}
		cw.Flush()
		if !cw.storable() || cw.statusCode() != http.StatusCreated || cw.body.String() != "hello" ||
			cw.headers.Get("X-Thing") != "1" || cw.headers.Get("X-Request-Id") != "" {
			t.Fatalf("captured %d %v %q, storable %v", cw.statusCode(), cw.headers, cw.body.String(), cw.storable())
		}
		if rec.Code != http.StatusCreated || rec.Body.String() != "hello" || cw.Unwrap() != rec {
			t.Fatalf("client got %d %q", rec.Code, rec.Body)
		}
	})
	t.Run("write without header is 200", func(t *testing.T) {
		t.Parallel()
		cw := &captureWriter{ResponseWriter: httptest.NewRecorder(), limit: 10}
		_, _ = cw.Write([]byte("x"))
		if cw.statusCode() != http.StatusOK || !cw.storable() {
			t.Fatalf("status %d", cw.statusCode())
		}
	})
	t.Run("nothing written is 200 with the headers set so far", func(t *testing.T) {
		t.Parallel()
		cw := &captureWriter{ResponseWriter: httptest.NewRecorder(), limit: 10}
		cw.Header().Set("X-Thing", "2")
		if !cw.storable() || cw.statusCode() != http.StatusOK || cw.headers.Get("X-Thing") != "2" {
			t.Fatalf("status %d headers %v", cw.statusCode(), cw.headers)
		}
	})
	t.Run("flush before write", func(t *testing.T) {
		t.Parallel()
		cw := &captureWriter{ResponseWriter: httptest.NewRecorder(), limit: 10}
		cw.Flush()
		if cw.statusCode() != http.StatusOK {
			t.Fatalf("status %d", cw.statusCode())
		}
	})
	t.Run("informational status is not final", func(t *testing.T) {
		t.Parallel()
		cw := &captureWriter{ResponseWriter: httptest.NewRecorder(), limit: 10}
		cw.WriteHeader(http.StatusEarlyHints)
		cw.WriteHeader(http.StatusAccepted)
		if cw.statusCode() != http.StatusAccepted {
			t.Fatalf("status %d", cw.statusCode())
		}
	})
	t.Run("server errors and oversize bodies are not stored", func(t *testing.T) {
		t.Parallel()
		cw := &captureWriter{ResponseWriter: httptest.NewRecorder(), limit: 4}
		cw.WriteHeader(http.StatusInternalServerError)
		if cw.storable() {
			t.Fatal("a 500 is storable")
		}
		cw = &captureWriter{ResponseWriter: httptest.NewRecorder(), limit: 4}
		_, _ = cw.Write([]byte("abc"))
		_, _ = cw.Write([]byte("de"))
		if cw.storable() || cw.body.Len() != 0 {
			t.Fatalf("an oversize body is storable (kept %d bytes)", cw.body.Len())
		}
	})
}

func TestWriteReplay(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	rec.Header().Set("X-Request-Id", "req_new")
	writeReplay(rec, stored{
		status:  http.StatusCreated,
		headers: http.Header{"Etag": {`"1"`}, "Content-Type": {"application/json"}},
		body:    []byte(`{"a":1}`),
	})
	if rec.Code != http.StatusCreated || rec.Body.String() != `{"a":1}` || rec.Header().Get("Etag") != `"1"` ||
		rec.Header().Get(HeaderReplayed) != "true" || rec.Header().Get("X-Request-Id") != "req_new" {
		t.Fatalf("replay: %d %v %s", rec.Code, rec.Header(), rec.Body)
	}
}
