package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

	"go.opentelemetry.io/otel/trace"

	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/scope"
)

// ProblemTypeBase prefixes every problem type URI: the type of a problem with
// code "not_found" is ProblemTypeBase + "not_found". docs.portun.io is a
// placeholder domain until decision D-01 secures the product domain.
const ProblemTypeBase = "https://docs.portun.io/errors/"

// Content types.
const (
	ContentTypeJSON    = "application/json"
	ContentTypeProblem = "application/problem+json"
)

// Problem is an RFC 9457 problem details body (ADR-0009). Instance is the
// request path (a URI reference to the resource the request addressed); the
// specific occurrence is identified by RequestID and TraceID.
type Problem struct {
	Type      string            `json:"type"`
	Title     string            `json:"title"`
	Status    int               `json:"status"`
	Code      errs.Code         `json:"code"`
	Detail    string            `json:"detail,omitempty"`
	Instance  string            `json:"instance,omitempty"`
	TraceID   string            `json:"trace_id,omitempty"`
	RequestID string            `json:"request_id,omitempty"`
	Errors    []errs.FieldError `json:"errors,omitempty"`
}

// WriteProblem writes err as problem details. The body holds only the
// client-safe view of err (errs.Public); internal causes never appear in it.
// For 5xx errors even the message is withheld: the detail is the code's
// title and there are no field errors; the message is logged only.
// Errors without a known code become a generic 500. 5xx errors are logged at
// error level with their cause, 4xx at debug level.
func WriteProblem(w http.ResponseWriter, r *http.Request, err error) {
	p, err := problemFor(r, err)
	logProblem(r, p, err)
	sendProblem(w, p)
}

// problemFor builds the problem for err and returns the (possibly mapped)
// error that it describes.
func problemFor(r *http.Request, err error) (Problem, error) {
	if err == nil {
		err = errs.New(errs.CodeInternal, "internal error")
	}
	var pe *errs.Error
	var mbe *http.MaxBytesError
	if !errors.As(err, &pe) && errors.As(err, &mbe) {
		err = errs.Wrap(errs.CodePayloadTooLarge, err, fmt.Sprintf("Request body must not exceed %d bytes.", mbe.Limit))
	}
	code, detail, fields := errs.Public(err)
	if !code.Known() {
		code = errs.CodeInternal
	}
	if code.HTTPStatus() >= http.StatusInternalServerError {
		// A server-side message can describe internals (roles, hosts,
		// configuration); only the log gets it.
		detail, fields = code.Title(), nil
	}
	p := Problem{
		Type:   ProblemTypeBase + string(code),
		Title:  code.Title(),
		Status: code.HTTPStatus(),
		Code:   code,
		Detail: detail,
		Errors: fields,
	}
	if r != nil {
		ctx := r.Context()
		if st := stateFrom(ctx); st != nil {
			ctx = st.enrich(ctx)
		}
		p.Instance = r.URL.EscapedPath()
		if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
			p.TraceID = sc.TraceID().String()
		}
		if req, ok := scope.RequestFrom(ctx); ok {
			p.RequestID = req.RequestID
		}
	}
	return p, err
}

func logProblem(r *http.Request, p Problem, err error) {
	if r == nil {
		return // WriteProblem requires a request; problemFor tolerates nil for safety
	}
	ctx := r.Context()
	if st := stateFrom(ctx); st != nil {
		ctx = st.enrich(ctx)
	}
	level, msg := levelFor(p.Status), "request failed"
	if errors.Is(err, context.Canceled) && r.Context().Err() != nil {
		// The client went away; this is not a server fault worth an error.
		level, msg = slog.LevelInfo, "request canceled by client"
	}
	loggerOf(r).LogAttrs(ctx, level, msg,
		slog.String("code", string(p.Code)),
		slog.Int("status", p.Status),
		slog.String("method", truncate(r.Method, 32)),
		slog.String("route", Route(r)),
		slog.String("path", truncate(r.URL.Path, maxLoggedPath)),
		slog.String("error", err.Error()),
	)
}

func levelFor(status int) slog.Level {
	if status >= http.StatusInternalServerError {
		return slog.LevelError
	}
	return slog.LevelDebug
}

// staleHeaders are response headers a handler may have set for the response
// it meant to send; they must not describe a problem response.
var staleHeaders = []string{ //nolint:gochecknoglobals // constant list
	"Set-Cookie", "Content-Encoding", "Content-Disposition", "Content-Range",
	"Location", "ETag", "Last-Modified", "Content-Language",
}

// sendProblem writes p. Problems are never cached. Headers the handler set
// for its own response (cookies, encodings, redirects, validators) are
// removed; security headers, X-Request-Id, CORS headers and Vary are kept.
func sendProblem(w http.ResponseWriter, p Problem) {
	body, err := json.Marshal(p)
	if err != nil {
		// Problem has only strings, ints and string slices; this cannot fail.
		body = []byte(`{"type":"` + ProblemTypeBase + `internal","title":"Internal error","status":500,"code":"internal"}`)
		p.Status = http.StatusInternalServerError
	}
	body = append(body, '\n')
	h := w.Header()
	for _, k := range staleHeaders {
		h.Del(k)
	}
	h.Set("Content-Type", ContentTypeProblem)
	h.Set("Content-Length", strconv.Itoa(len(body)))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(p.Status)
	_, _ = w.Write(body) // the client may be gone; nothing useful to do
}

// genericInternalProblem is used where no request is available.
func genericInternalProblem() Problem {
	return Problem{
		Type:   ProblemTypeBase + string(errs.CodeInternal),
		Title:  errs.CodeInternal.Title(),
		Status: errs.CodeInternal.HTTPStatus(),
		Code:   errs.CodeInternal,
		Detail: errs.CodeInternal.Title(),
	}
}
