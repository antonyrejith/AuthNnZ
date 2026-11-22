package httpx

import (
	"net/http"
	"strconv"

	"github.com/antonyrejith/portun/internal/platform/errs"
)

// CSRFConfig configures CSRF.
type CSRFConfig struct {
	// TrustedOrigins may send unsafe cross-origin requests
	// ("https://console.example.com"); they are normalized like CORS origins.
	TrustedOrigins []string
}

// CSRF protects cookie-authenticated browser endpoints against cross-site
// request forgery with net/http.CrossOriginProtection: unsafe requests
// (anything but GET, HEAD and OPTIONS) that the browser marks as
// cross-origin (Sec-Fetch-Site, or an Origin that does not match Host) are
// rejected with a 403 permission_denied problem, unless their Origin is
// trusted. Requests without these headers (non-browser clients) pass.
func CSRF(cfg CSRFConfig) (Middleware, error) {
	cop := http.NewCrossOriginProtection()
	for _, o := range cfg.TrustedOrigins {
		n, ok := normalizeOrigin(o)
		if !ok {
			return nil, errs.New(errs.CodeInvalidRequest, "invalid trusted origin "+strconv.Quote(o))
		}
		if err := cop.AddTrustedOrigin(n); err != nil {
			return nil, errs.Wrap(errs.CodeInvalidRequest, err, "invalid trusted origin "+strconv.Quote(o))
		}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if err := cop.Check(r); err != nil {
				WriteProblem(w, r, errs.Wrap(errs.CodePermissionDenied, err, "Cross-origin request rejected."))
				return
			}
			next.ServeHTTP(w, r)
		})
	}, nil
}
