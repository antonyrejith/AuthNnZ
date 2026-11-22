package httpx_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/antonyrejith/portun/internal/platform/httpx"
	"github.com/antonyrejith/portun/internal/platform/log"
)

// syncBuffer is a goroutine-safe log sink.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// records returns the JSON log records.
func (b *syncBuffer) records(t *testing.T) []map[string]any {
	t.Helper()
	var out []map[string]any
	sc := bufio.NewScanner(strings.NewReader(b.String()))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("log line is not JSON: %v: %s", err, sc.Text())
		}
		out = append(out, m)
	}
	return out
}

// find returns the first record with the given message.
func (b *syncBuffer) find(t *testing.T, msg string) map[string]any {
	t.Helper()
	for _, r := range b.records(t) {
		if r["msg"] == msg {
			return r
		}
	}
	t.Fatalf("no log record %q in:\n%s", msg, b.String())
	return nil
}

func newLogger() (*slog.Logger, *syncBuffer) {
	buf := &syncBuffer{}
	return log.New(buf, slog.LevelDebug), buf
}

func decodeProblem(t *testing.T, body io.Reader) httpx.Problem {
	t.Helper()
	var p httpx.Problem
	if err := json.NewDecoder(body).Decode(&p); err != nil {
		t.Fatalf("decode problem: %v", err)
	}
	return p
}

func serve(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}
