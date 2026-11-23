// Package apimw is the middleware shared by every generated API (ADR-0009,
// ADR-0018): the operation table built from the OpenAPI specification, bearer
// authentication, authorization against the operation's permission, change
// reasons for the audit trail, the route label for logs and metrics, and the
// rendering of every error as an RFC 9457 problem.
//
// # Composition
//
// An API server wraps its generated ogen server like this (outermost first):
//
//	httpx.Chain(server,
//		auth.Middleware(),           // 401 before anything else is read or stored
//		idempotency.Middleware(...), // POST replay (platform: nil scope)
//	)
//
// and passes Middleware(ops, authorizer) to the generated server with
// WithMiddleware, ErrorHandler(ops) to WithErrorHandler, and NotFound and
// MethodNotAllowed to WithNotFound and WithMethodNotAllowed.
//
// # Authentication (release B0)
//
// Authenticator accepts one bootstrap operator bearer token, configured as its
// SHA-256 digest (environment variable PORTUN_API_OPERATOR_TOKEN_SHA256). It
// is the B0 bootstrap credential: release B1 (T1.8) replaces it with OIDC
// client credentials and per-scope administrator authorization. The token is
// compared in constant time and is never logged. Without a configured digest
// every request is rejected.
//
// # Operation table
//
// ParseOperations reads the vendor extensions x-required-permission,
// x-audit-event and x-audit-reason of every operation from the embedded
// specification at startup and fails when one is missing or malformed, so an
// operation cannot be served without a permission and an audit event.
package apimw
