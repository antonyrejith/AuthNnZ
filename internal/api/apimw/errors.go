package apimw

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/ogen-go/ogen/ogenerrors"
	"github.com/ogen-go/ogen/openapi"
	"github.com/ogen-go/ogen/validate"

	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/httpx"
)

// headerIfMatch is the header of optimistic concurrency. The platform has no
// 428 error code, so a missing or unusable If-Match is a failed precondition
// (412) like a stale one.
const headerIfMatch = "If-Match"

// WriteProblem writes err as an RFC 9457 problem (httpx.WriteProblem). An
// unauthenticated problem carries WWW-Authenticate: Bearer.
func WriteProblem(w http.ResponseWriter, r *http.Request, err error) {
	if errs.Is(err, errs.CodeUnauthenticated) {
		w.Header().Set("WWW-Authenticate", "Bearer")
	}
	httpx.WriteProblem(w, r, err)
}

// ErrorHandler is the ogen error handler of every generated API: security,
// parameter, body and handler errors all become problems. Handler errors
// (*errs.Error) pass through; decode errors become invalid_request (400),
// validation errors validation_failed (422) with JSON pointers, and a failed
// security requirement unauthenticated (401). Values sent by the client are
// never echoed in a detail. Errors found before the middleware ran (security,
// parameters, body) still get the operation's route label.
func ErrorHandler(ops Operations) ogenerrors.ErrorHandler {
	return func(ctx context.Context, w http.ResponseWriter, r *http.Request, err error) {
		var oe ogenerrors.Error
		if errors.As(err, &oe) {
			if info, ok := ops.Info(oe.OperationID()); ok {
				httpx.SetRoute(ctx, info.PathTemplate)
			}
		}
		WriteProblem(w, r, Translate(err))
	}
}

// NotFound answers a request that matches no operation with a 404 problem.
func NotFound(w http.ResponseWriter, r *http.Request) {
	WriteProblem(w, r, errs.New(errs.CodeNotFound, "No resource matches the request path."))
}

// MethodNotAllowed answers a request whose method the path does not support
// with a 405 problem and the Allow header.
func MethodNotAllowed(w http.ResponseWriter, r *http.Request, allowed string) {
	w.Header().Set("Allow", allowed)
	WriteProblem(w, r, errs.New(errs.CodeMethodNotAllowed, "The method is not allowed for this resource."))
}

// Translate maps an error from the generated server to a platform error.
func Translate(err error) error {
	var pe *errs.Error
	var mbe *http.MaxBytesError
	switch {
	case errors.As(err, &pe):
		return err
	case errors.As(err, &mbe):
		return err // httpx.WriteProblem maps it to payload_too_large
	}
	var (
		secErr    *ogenerrors.SecurityError
		paramsErr *ogenerrors.DecodeParamsError
		reqErr    *ogenerrors.DecodeRequestError
		ctErr     *validate.InvalidContentTypeError
	)
	switch {
	case errors.As(err, &secErr):
		return errUnauthenticated()
	case errors.As(err, &ctErr):
		return errs.Wrap(errs.CodeInvalidRequest, err, "The request has an unsupported Content-Type.")
	case errors.As(err, &paramsErr):
		return translateParams(err)
	case errors.As(err, &reqErr):
		return translateBody(err)
	}
	return err // not a client error: httpx.WriteProblem turns it into a generic 500
}

func translateParams(err error) error {
	var pe *ogenerrors.DecodeParamError
	if !errors.As(err, &pe) {
		return errs.Wrap(errs.CodeInvalidRequest, err, "The request parameters are malformed.")
	}
	code, detail := fieldProblem(pe.Err)
	if pe.In == openapi.LocationHeader && strings.EqualFold(pe.Name, headerIfMatch) {
		return errs.Wrap(errs.CodePreconditionFailed, err, headerIfMatch+" is required and must be the current ETag of the resource.")
	}
	pointer := "/" + parameterGroup(pe.In) + "/" + pe.Name
	return errs.Wrap(errs.CodeValidationFailed, err, "The request contains invalid parameters.").
		WithFields(errs.FieldError{Pointer: pointer, Code: code, Detail: detail})
}

func parameterGroup(in openapi.ParameterLocation) string {
	switch in {
	case openapi.LocationHeader:
		return "headers"
	case openapi.LocationQuery:
		return "query"
	case openapi.LocationPath:
		return "path"
	case openapi.LocationCookie:
		return "cookies"
	}
	return "parameters"
}

func translateBody(err error) error {
	var ve *validate.Error
	if errors.As(err, &ve) {
		var fields []errs.FieldError
		flatten("", ve, &fields)
		return errs.Wrap(errs.CodeValidationFailed, err, "The request contains invalid fields.").WithFields(fields...)
	}
	return errs.Wrap(errs.CodeInvalidRequest, err, "The request body is missing, is not valid JSON or does not have the expected shape.")
}

// flatten turns validation errors into field errors with JSON pointers.
func flatten(prefix string, ve *validate.Error, out *[]errs.FieldError) {
	for _, f := range ve.Fields {
		pointer := prefix + "/" + escapePointer(strings.Trim(f.Name, "[]"))
		var nested *validate.Error
		if errors.As(f.Error, &nested) && nested != ve {
			flatten(pointer, nested, out)
			continue
		}
		code, detail := fieldProblem(f.Error)
		*out = append(*out, errs.FieldError{Pointer: pointer, Code: code, Detail: detail})
	}
	if len(ve.Fields) == 0 {
		*out = append(*out, errs.FieldError{Pointer: prefix, Code: "invalid", Detail: "the value is invalid"})
	}
}

// fieldProblem returns a code and a detail for a single validation failure.
func fieldProblem(err error) (code, detail string) {
	var (
		minLen *validate.MinLengthError
		maxLen *validate.MaxLengthError
		regex  *validate.NoRegexMatchError
		ve     *validate.Error
	)
	switch {
	case errors.Is(err, validate.ErrFieldRequired):
		return "required", "the value is required"
	case errors.As(err, &minLen):
		return "too_short", "must have at least " + strconv.Itoa(minLen.MinLength) + " characters"
	case errors.As(err, &maxLen):
		return "too_long", "must have at most " + strconv.Itoa(maxLen.MaxLength) + " characters"
	case errors.As(err, &regex):
		return "invalid_format", "does not have the expected format"
	case errors.As(err, &ve):
		for _, f := range ve.Fields {
			if c, d := fieldProblem(f.Error); c != "invalid" {
				return c, d
			}
		}
	}
	return "invalid", "the value is invalid"
}

// escapePointer escapes a JSON Pointer token (RFC 6901).
func escapePointer(s string) string {
	return strings.NewReplacer("~", "~0", "/", "~1").Replace(s)
}
