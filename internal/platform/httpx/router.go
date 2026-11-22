package httpx

import (
	"net/http"

	"github.com/antonyrejith/portun/internal/platform/errs"
)

// Router is a net/http.ServeMux (Go 1.22+ "METHOD /path/{name}" patterns)
// that records the matched pattern for telemetry and logs (see Route) and
// answers unmatched requests with problem details instead of plain text:
// 404 not_found, and 405 with an Allow header.
type Router struct {
	mux *http.ServeMux
}

// NewRouter returns an empty router.
func NewRouter() *Router { return &Router{mux: http.NewServeMux()} }

// Handle registers h for pattern. Like ServeMux.Handle, it panics on an
// invalid or conflicting pattern (a programmer error found at startup).
func (rt *Router) Handle(pattern string, h http.Handler) { rt.mux.Handle(pattern, h) }

// HandleFunc registers f for pattern.
func (rt *Router) HandleFunc(pattern string, f func(http.ResponseWriter, *http.Request)) {
	rt.mux.HandleFunc(pattern, f)
}

// ServeHTTP dispatches the request.
func (rt *Router) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if st := stateFrom(r.Context()); st != nil {
		// ServeMux sets r.Pattern on this request before calling the handler.
		defer func() {
			if r.Pattern != "" {
				st.setRoute(routeTemplate(r.Pattern))
			}
		}()
	}
	if _, pattern := rt.mux.Handler(r); pattern == "" {
		// No pattern: ServeMux will answer 404, 405 or a path-cleaning
		// redirect. Replace the plain-text errors with problems.
		rt.mux.ServeHTTP(&unmatchedWriter{ResponseWriter: w, r: r}, r)
		return
	}
	rt.mux.ServeHTTP(w, r)
}

type unmatchedWriter struct {
	http.ResponseWriter
	r        *http.Request
	replaced bool
}

func (u *unmatchedWriter) WriteHeader(code int) {
	switch code {
	case http.StatusNotFound:
		u.replaced = true
		WriteProblem(u.ResponseWriter, u.r, errs.New(errs.CodeNotFound, "No resource matches the request path."))
	case http.StatusMethodNotAllowed:
		u.replaced = true
		// errs has no 405 code yet: the status stays 405 (with the Allow header
		// ServeMux set) and the code is invalid_request.
		p, err := problemFor(u.r, errs.New(errs.CodeInvalidRequest, "The method is not allowed for this resource."))
		p.Status = http.StatusMethodNotAllowed
		p.Title = "Method not allowed"
		logProblem(u.r, p, err)
		sendProblem(u.ResponseWriter, p)
	default:
		u.ResponseWriter.WriteHeader(code)
	}
}

func (u *unmatchedWriter) Write(b []byte) (int, error) {
	if u.replaced {
		return len(b), nil
	}
	return u.ResponseWriter.Write(b) //nolint:wrapcheck // pass-through writer
}

func (u *unmatchedWriter) Unwrap() http.ResponseWriter { return u.ResponseWriter }
