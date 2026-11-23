package apimw

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ogen-go/ogen/middleware"

	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/httpx"
	"github.com/antonyrejith/portun/internal/platform/scope"
)

// Change header names and limits (ADR-0009).
const (
	HeaderChangeReason = "Change-Reason"
	HeaderChangeTicket = "Change-Ticket"
	MaxChangeReason    = 500
	MaxChangeTicket    = 100
)

type ctxKey int

const operationKey ctxKey = 0

// WithOperation returns ctx carrying the operation being served.
func WithOperation(ctx context.Context, info OperationInfo) context.Context {
	return context.WithValue(ctx, operationKey, info)
}

// OperationFrom returns the operation being served, for handlers and audit
// code that need its x-audit-event.
func OperationFrom(ctx context.Context) (OperationInfo, bool) {
	info, ok := ctx.Value(operationKey).(OperationInfo)
	return info, ok
}

// Middleware returns the ogen middleware shared by every generated API. It
// runs after the request's security, parameters and body were checked and,
// for each operation:
//
//  1. sets the route label (the path template) for logs and metrics;
//  2. checks that an actor is present (Authenticator.Middleware ran) and that
//     authz allows the operation's x-required-permission (403 otherwise);
//  3. copies Change-Reason and Change-Ticket into the scope.Request, where
//     the audit module reads them, and rejects a missing reason for
//     x-audit-reason: required and over-long values (422, pointer
//     /headers/Change-Reason);
//  4. puts the OperationInfo into the context (OperationFrom).
func Middleware(ops Operations, authz Authorizer) middleware.Middleware {
	return func(req middleware.Request, next middleware.Next) (middleware.Response, error) {
		ctx := req.Context
		info, ok := ops.Info(req.OperationID)
		if !ok {
			return middleware.Response{}, errs.New(errs.CodeInternal, "api: operation "+req.OperationID+" is not in the operation table")
		}
		httpx.SetRoute(ctx, info.PathTemplate)
		actor, ok := scope.ActorFrom(ctx)
		if !ok {
			return middleware.Response{}, errUnauthenticated()
		}
		if !authz.Allowed(ctx, actor, info.Permission) {
			return middleware.Response{}, errs.New(errs.CodePermissionDenied, "You are not allowed to perform this operation.")
		}
		var headers http.Header
		if req.Raw != nil {
			headers = req.Raw.Header
		}
		reason, ticket, err := changeHeaders(headers, info.ReasonRequired)
		if err != nil {
			return middleware.Response{}, err
		}
		sr, _ := scope.RequestFrom(ctx)
		sr.Operation, sr.ChangeReason, sr.ChangeTicket = info.OperationID, reason, ticket
		ctx = scope.WithRequest(WithOperation(ctx, info), sr)
		req.Context = ctx
		return next(req)
	}
}

// changeHeaders reads and checks Change-Reason and Change-Ticket.
func changeHeaders(h http.Header, reasonRequired bool) (reason, ticket string, err error) {
	var fields []errs.FieldError
	reason, f := changeValue(h, HeaderChangeReason, MaxChangeReason)
	if f != nil {
		fields = append(fields, *f)
	}
	if reason == "" && f == nil && reasonRequired {
		fields = append(fields, errs.FieldError{
			Pointer: "/headers/" + HeaderChangeReason, Code: "required",
			Detail: "this operation requires a " + HeaderChangeReason + " header",
		})
	}
	ticket, f = changeValue(h, HeaderChangeTicket, MaxChangeTicket)
	if f != nil {
		fields = append(fields, *f)
	}
	if len(fields) > 0 {
		return "", "", errs.New(errs.CodeValidationFailed, "The request contains invalid headers.").WithFields(fields...)
	}
	return reason, ticket, nil
}

// changeValue returns the trimmed value of header name, or a field error when
// it repeats, is not valid UTF-8, has control characters or is too long.
func changeValue(h http.Header, name string, maxLen int) (string, *errs.FieldError) {
	values := h.Values(name)
	if len(values) == 0 {
		return "", nil
	}
	pointer := "/headers/" + name
	if len(values) > 1 {
		return "", &errs.FieldError{Pointer: pointer, Code: "invalid", Detail: name + " must be given once"}
	}
	v := strings.TrimSpace(values[0])
	switch {
	case !utf8.ValidString(v) || strings.IndexFunc(v, unicode.IsControl) >= 0:
		return "", &errs.FieldError{Pointer: pointer, Code: "invalid", Detail: name + " must be text without control characters"}
	case utf8.RuneCountInString(v) > maxLen:
		return "", &errs.FieldError{Pointer: pointer, Code: "too_long", Detail: name + " must have at most " + strconv.Itoa(maxLen) + " characters"}
	}
	return v, nil
}
