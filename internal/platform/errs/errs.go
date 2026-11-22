// Package errs defines the platform's error codes and error type.
//
// Every error that can reach a client carries a stable Code. One table maps
// codes to HTTP status and title (ADR-0009). The message of an Error is safe
// to show to clients; its cause is internal and never leaves the service.
package errs

import (
	"context"
	"errors"
	"net/http"
)

// StatusClientClosedRequest (499) is the de-facto "client closed request"
// status, recorded for CodeCanceled. The client never sees it (it has gone);
// it keeps cancellations out of 5xx error rates in logs and metrics.
const StatusClientClosedRequest = 499

// Code is a stable, machine-readable error code.
type Code string

// Error codes. Clients branch on these values; they never change meaning.
const (
	CodeInvalidRequest     Code = "invalid_request"     // malformed request
	CodeValidationFailed   Code = "validation_failed"   // well-formed but invalid fields
	CodeUnauthenticated    Code = "unauthenticated"     // no valid credentials
	CodePermissionDenied   Code = "permission_denied"   // authenticated but not allowed
	CodeNotFound           Code = "not_found"           // missing or not visible to this tenant
	CodeMethodNotAllowed   Code = "method_not_allowed"  // the resource does not support the method
	CodeConflict           Code = "conflict"            // state conflict (including already exists)
	CodePreconditionFailed Code = "precondition_failed" // stale version (If-Match)
	CodePayloadTooLarge    Code = "payload_too_large"
	CodeRateLimited        Code = "rate_limited"
	CodeUnavailable        Code = "unavailable"       // dependency down; retry later
	CodeDeadlineExceeded   Code = "deadline_exceeded" // operation timed out
	CodeCanceled           Code = "canceled"          // the client canceled the request (context.Canceled)
	CodeInternal           Code = "internal"          // unexpected failure
)

var codeInfo = map[Code]struct { //nolint:gochecknoglobals // immutable lookup table
	status int
	title  string
}{
	CodeInvalidRequest:     {http.StatusBadRequest, "Malformed request"},
	CodeValidationFailed:   {http.StatusUnprocessableEntity, "Request validation failed"},
	CodeUnauthenticated:    {http.StatusUnauthorized, "Authentication required"},
	CodePermissionDenied:   {http.StatusForbidden, "Permission denied"},
	CodeNotFound:           {http.StatusNotFound, "Not found"},
	CodeMethodNotAllowed:   {http.StatusMethodNotAllowed, "Method not allowed"},
	CodeConflict:           {http.StatusConflict, "Conflict"},
	CodePreconditionFailed: {http.StatusPreconditionFailed, "Precondition failed"},
	CodePayloadTooLarge:    {http.StatusRequestEntityTooLarge, "Payload too large"},
	CodeRateLimited:        {http.StatusTooManyRequests, "Too many requests"},
	CodeUnavailable:        {http.StatusServiceUnavailable, "Service unavailable"},
	CodeDeadlineExceeded:   {http.StatusGatewayTimeout, "Deadline exceeded"},
	CodeCanceled:           {StatusClientClosedRequest, "Client closed request"},
	CodeInternal:           {http.StatusInternalServerError, "Internal error"},
}

// HTTPStatus returns the HTTP status for the code (500 for unknown codes).
func (c Code) HTTPStatus() int {
	if info, ok := codeInfo[c]; ok {
		return info.status
	}
	return http.StatusInternalServerError
}

// Title returns the short human-readable summary for the code.
func (c Code) Title() string {
	if info, ok := codeInfo[c]; ok {
		return info.title
	}
	return codeInfo[CodeInternal].title
}

// Known reports whether c is one of the defined codes.
func (c Code) Known() bool {
	_, ok := codeInfo[c]
	return ok
}

// AllCodes lists every defined code.
func AllCodes() []Code {
	out := make([]Code, 0, len(codeInfo))
	for c := range codeInfo {
		out = append(out, c)
	}
	return out
}

// FieldError describes one invalid field, located by a JSON Pointer (RFC 6901).
type FieldError struct {
	Pointer string `json:"pointer"`
	Code    string `json:"code"`
	Detail  string `json:"detail"`
}

// Error is the platform error type.
type Error struct {
	code    Code
	message string
	cause   error
	fields  []FieldError
}

// New returns an error with a client-safe message.
func New(code Code, message string) *Error {
	return &Error{code: code, message: message}
}

// Wrap returns an error with a client-safe message and an internal cause.
func Wrap(code Code, cause error, message string) *Error {
	return &Error{code: code, message: message, cause: cause}
}

// WithFields returns a copy of e with field errors attached. On a nil *Error
// it returns an internal error carrying the fields.
func (e *Error) WithFields(fields ...FieldError) *Error {
	if e == nil {
		e = &Error{code: CodeInternal, message: CodeInternal.Title()}
	}
	c := *e
	c.fields = append(append([]FieldError(nil), e.fields...), fields...)
	return &c
}

// Error implements error. It includes the internal cause and is for logs only.
// A typed-nil *Error (a nil pointer stored in an error interface, which is
// not a nil error) reports itself as internal; no method panics on it.
func (e *Error) Error() string {
	if e == nil {
		return string(CodeInternal) + ": nil *errs.Error"
	}
	if e.cause == nil {
		return string(e.code) + ": " + e.message
	}
	return string(e.code) + ": " + e.message + ": " + e.cause.Error()
}

// Unwrap returns the internal cause.
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// Code returns the error code (CodeInternal for a nil *Error).
func (e *Error) Code() Code {
	if e == nil {
		return CodeInternal
	}
	return e.code
}

// Message returns the client-safe message.
func (e *Error) Message() string {
	if e == nil {
		return CodeInternal.Title()
	}
	return e.message
}

// Fields returns the field errors.
func (e *Error) Fields() []FieldError {
	if e == nil {
		return nil
	}
	return append([]FieldError(nil), e.fields...)
}

// CodeOf returns the code of the first *Error in err's chain. Context
// cancellation (CodeCanceled) and deadlines map to their own codes; anything
// else unknown, including a typed-nil *Error, is CodeInternal. A nil error
// has no code ("").
func CodeOf(err error) Code {
	if err == nil {
		return ""
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Code()
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return CodeDeadlineExceeded
	case errors.Is(err, context.Canceled):
		return CodeCanceled
	default:
		return CodeInternal
	}
}

// Is reports whether err carries the given code.
func Is(err error, code Code) bool {
	return err != nil && CodeOf(err) == code
}

// Public returns the client-safe view of err: its code, message and field
// errors. Errors that are not *Error become a generic internal error, so
// internal details never leak.
func Public(err error) (Code, string, []FieldError) {
	var e *Error
	if errors.As(err, &e) && e != nil {
		return e.code, e.message, e.Fields()
	}
	code := CodeOf(err)
	return code, code.Title(), nil
}
