package idempotency

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"

	"github.com/google/uuid"

	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/httpx"
)

// Header names.
const (
	HeaderKey      = "Idempotency-Key"
	HeaderReplayed = "Idempotent-Replayed"
)

// maxKeyLength is the longest key.
const maxKeyLength = 255

// PlatformScope is the scope of platform APIs: the nil UUID.
func PlatformScope(*http.Request) uuid.UUID { return uuid.Nil }

// unstoredHeaders are response headers that belong to one request and are not
// replayed.
var unstoredHeaders = []string{httpx.HeaderRequestID, HeaderReplayed, "Date", "Traceparent", "Tracestate"} //nolint:gochecknoglobals // constant list

// Middleware makes POST requests with an Idempotency-Key header idempotent
// (see the package documentation). Other requests, and POST requests without
// the header, pass through. scopeOf returns the scope of the request's keys
// (PlatformScope for platform APIs); it must be derived from the
// authenticated caller or tenant, so run the middleware after authentication.
func (s *Store) Middleware(scopeOf func(*http.Request) uuid.UUID) httpx.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			values := r.Header.Values(HeaderKey)
			if r.Method != http.MethodPost || len(values) == 0 {
				next.ServeHTTP(w, r)
				return
			}
			key, err := validKey(values)
			if err != nil {
				httpx.WriteProblem(w, r, err)
				return
			}
			body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.maxBody))
			if err != nil {
				httpx.WriteProblem(w, r, err)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			s.serve(next, w, r, scopeOf(r), key, requestHash(r, body))
		})
	}
}

func (s *Store) serve(next http.Handler, w http.ResponseWriter, r *http.Request, scopeID uuid.UUID, key, hash string) {
	out, rec, err := s.claim(r.Context(), scopeID, key, hash)
	if err != nil {
		httpx.WriteProblem(w, r, err)
		return
	}
	switch out {
	case replay:
		writeReplay(w, rec)
		return
	case mismatch:
		httpx.WriteProblem(w, r, errs.New(errs.CodeValidationFailed, "The request is invalid.").WithFields(errs.FieldError{
			Pointer: "/headers/" + HeaderKey, Code: "reused", Detail: "Idempotency-Key reused with a different request",
		}))
		return
	case inProgress:
		httpx.WriteProblem(w, r, errs.New(errs.CodeConflict, "A request with this Idempotency-Key is still being processed."))
		return
	case claimed:
	}

	cw := &captureWriter{ResponseWriter: w, limit: s.maxBytes}
	finished := false
	// A panic in the handler releases the key and goes on.
	defer s.releaseUnlessFinished(r.Context(), &finished, scopeID, key, hash)
	next.ServeHTTP(cw, r)
	finished = true
	var result *stored
	if cw.storable() {
		result = &stored{status: cw.statusCode(), headers: cw.headers, body: cw.body.Bytes()}
	}
	s.finish(r.Context(), scopeID, key, hash, result)
}

// releaseUnlessFinished releases the key when the handler did not return.
func (s *Store) releaseUnlessFinished(ctx context.Context, finished *bool, scopeID uuid.UUID, key, hash string) {
	if !*finished {
		s.finish(ctx, scopeID, key, hash, nil)
	}
}

// finish stores result, or releases the key when result is nil. It runs even
// when the client has gone.
func (s *Store) finish(ctx context.Context, scopeID uuid.UUID, key, hash string, result *stored) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), writeTimeout)
	defer cancel()
	var err error
	if result != nil {
		err = s.complete(ctx, scopeID, key, hash, *result)
	} else {
		err = s.release(ctx, scopeID, key, hash)
	}
	if err != nil {
		// The client already has its response; an unreleased key expires with
		// its lease.
		s.logger.ErrorContext(ctx, "idempotency bookkeeping failed", slog.Any("error", err))
	}
}

func writeReplay(w http.ResponseWriter, rec stored) {
	h := w.Header()
	for k, vs := range rec.headers {
		h.Del(k)
		for _, v := range vs {
			h.Add(k, v)
		}
	}
	h.Set(HeaderReplayed, "true")
	w.WriteHeader(rec.status)
	_, _ = w.Write(rec.body) // the client may be gone; nothing useful to do
}

// validKey checks the Idempotency-Key header values: exactly one value of 1
// to 255 printable ASCII characters.
func validKey(values []string) (string, error) {
	bad := func(detail string) error {
		return errs.New(errs.CodeValidationFailed, "The request contains invalid headers.").WithFields(errs.FieldError{
			Pointer: "/headers/" + HeaderKey, Code: "invalid", Detail: detail,
		})
	}
	if len(values) != 1 {
		return "", bad("Idempotency-Key must be given once")
	}
	key := values[0]
	if key == "" || len(key) > maxKeyLength {
		return "", bad("Idempotency-Key must have 1 to 255 characters")
	}
	for i := range len(key) {
		if c := key[i]; c < 0x20 || c > 0x7e {
			return "", bad("Idempotency-Key must contain only printable ASCII characters")
		}
	}
	return key, nil
}

// captureWriter forwards the response to the client and keeps a copy of its
// status, headers and body (up to limit bytes).
type captureWriter struct {
	http.ResponseWriter
	limit    int
	status   int
	headers  http.Header
	body     bytes.Buffer
	overflow bool
}

func (c *captureWriter) WriteHeader(code int) {
	if c.status == 0 && code >= http.StatusOK {
		c.status = code
		c.headers = c.ResponseWriter.Header().Clone()
		for _, k := range unstoredHeaders {
			c.headers.Del(k)
		}
	}
	c.ResponseWriter.WriteHeader(code)
}

func (c *captureWriter) Write(b []byte) (int, error) {
	if c.status == 0 {
		c.WriteHeader(http.StatusOK)
	}
	if !c.overflow {
		if c.body.Len()+len(b) > c.limit {
			c.overflow = true
			c.body.Reset()
		} else {
			c.body.Write(b)
		}
	}
	n, err := c.ResponseWriter.Write(b)
	return n, err //nolint:wrapcheck // a pass-through writer must return the underlying error unchanged
}

// Flush sends buffered data to the client, if the underlying writer can.
func (c *captureWriter) Flush() {
	if c.status == 0 {
		c.WriteHeader(http.StatusOK)
	}
	_ = http.NewResponseController(c.ResponseWriter).Flush()
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (c *captureWriter) Unwrap() http.ResponseWriter { return c.ResponseWriter }

// statusCode is the status of the response; net/http sends 200 when the
// handler wrote nothing.
func (c *captureWriter) statusCode() int {
	if c.status == 0 {
		return http.StatusOK
	}
	return c.status
}

// storable reports whether the response is kept: below 500 and within the
// size limit.
func (c *captureWriter) storable() bool {
	if c.status == 0 {
		// Nothing was written: the headers are those set so far.
		c.headers = c.ResponseWriter.Header().Clone()
		for _, k := range unstoredHeaders {
			c.headers.Del(k)
		}
	}
	return c.statusCode() < http.StatusInternalServerError && !c.overflow
}
