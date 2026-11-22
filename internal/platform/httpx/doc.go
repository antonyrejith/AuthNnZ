// Package httpx is the platform's HTTP layer: a server with secure timeouts
// and graceful shutdown, composable middleware, RFC 9457 problem details
// (ADR-0009), strict JSON decoding, health endpoints, routing helpers and the
// browser protections of SEC-09 (security headers, CORS, CSRF, hardened
// cookies).
//
// The recommended middleware order, outermost first, is:
//
//	handler := httpx.Chain(router,
//		httpx.Recover(logger),          // panics become 500 problems
//		httpx.RequestID(requestIDCfg),  // request ID, client IP, scope.Request
//		instrument,                     // httpx.Instrument: tracing and RED metrics
//		httpx.AccessLog(logger),        // one record per request
//		securityHeaders,                // httpx.SecurityHeaders
//		httpx.MaxBytes(0),              // request body limit (default 1 MiB)
//	)
//
// Route-group middleware such as CORS and CSRF wraps the handlers of that
// group. Use Router (a net/http.ServeMux) so that the matched route pattern is
// known to telemetry and logs, and unmatched requests get problem details.
//
// The middleware of one request share a small request-scoped state in the
// context (route, scheme, request scope, span context, logger, body limit), so
// that the outermost middleware can report what inner ones learned.
package httpx
