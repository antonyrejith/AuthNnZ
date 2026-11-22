package httpx

import (
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"reflect"
	"strconv"
	"strings"

	"github.com/antonyrejith/portun/internal/platform/errs"
)

// Field error codes used by DecodeJSON.
const (
	FieldCodeUnknownField  = "unknown_field"
	FieldCodeInvalidType   = "invalid_type"
	FieldCodeInvalidValue  = "invalid_value"
	FieldCodeMalformedJSON = "malformed_json"
	FieldCodeDuplicateName = "duplicate_field"
)

// DecodeJSON decodes the JSON request body into dst, which must be a non-nil
// pointer. It is strict on purpose: the content type must be
// application/json (optionally with charset=utf-8); the body must hold
// exactly one JSON value that is not null; unknown fields, duplicate fields,
// case-insensitive field matches and invalid UTF-8 are rejected; the body may
// not exceed the MaxBytes limit (DefaultMaxBodyBytes without the middleware).
//
// Failures are *errs.Error values with CodeInvalidRequest (or
// CodePayloadTooLarge) and, where the position is known, a field error whose
// Pointer is the RFC 6901 JSON Pointer of the offending value.
//
// Decoding uses the encoding/json v2 engine (Go 1.27 default) with the v1
// semantics for value formats, so request types behave as with
// encoding/json, but errors carry exact JSON Pointers.
func DecodeJSON(r *http.Request, dst any) error {
	if v := reflect.ValueOf(dst); v.Kind() != reflect.Pointer || v.IsNil() {
		return errs.New(errs.CodeInternal, "DecodeJSON needs a non-nil pointer")
	}
	if err := checkJSONContentType(r.Header.Get("Content-Type")); err != nil {
		return err
	}
	limit := DefaultMaxBodyBytes
	body := r.Body
	if st := stateFrom(r.Context()); st != nil && st.getMaxBodyBytes() > 0 {
		limit = st.getMaxBodyBytes() // MaxBytes already limits the body
	} else if body != nil {
		body = http.MaxBytesReader(nil, body, limit)
	}
	if r.ContentLength > limit {
		return tooLarge(limit, nil)
	}
	if body == nil || body == http.NoBody {
		return errs.New(errs.CodeInvalidRequest, "Request body is empty.")
	}
	opts := jsonv2.JoinOptions(
		json.DefaultOptionsV1(),
		json.ReportErrorsWithLegacySemantics(false),
		jsonv2.RejectUnknownMembers(true),
		jsonv2.MatchCaseInsensitiveNames(false),
		json.MatchCaseSensitiveDelimiter(true),
		jsontext.AllowDuplicateNames(false),
		jsontext.AllowInvalidUTF8(false),
	)
	dec := jsontext.NewDecoder(body, opts)
	if dec.PeekKind() == 'n' {
		return errs.New(errs.CodeInvalidRequest, "Request body must not be null.")
	}
	if err := jsonv2.UnmarshalDecode(dec, dst, opts); err != nil {
		if errors.Is(err, io.EOF) {
			return errs.Wrap(errs.CodeInvalidRequest, err, "Request body is empty.")
		}
		return decodeError(err)
	}
	if _, err := dec.ReadToken(); !errors.Is(err, io.EOF) {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return tooLarge(mbe.Limit, err)
		}
		return errs.New(errs.CodeInvalidRequest, "Request body must contain a single JSON value.")
	}
	return nil
}

func checkJSONContentType(v string) error {
	bad := errs.New(errs.CodeInvalidRequest, "Content-Type must be application/json.")
	if v == "" {
		return bad
	}
	mt, params, err := mime.ParseMediaType(v)
	if err != nil || mt != ContentTypeJSON {
		return bad
	}
	for k, val := range params {
		if k != "charset" || !strings.EqualFold(val, "utf-8") {
			return bad
		}
	}
	return nil
}

func tooLarge(limit int64, cause error) error {
	msg := fmt.Sprintf("Request body must not exceed %d bytes.", limit)
	if cause == nil {
		return errs.New(errs.CodePayloadTooLarge, msg)
	}
	return errs.Wrap(errs.CodePayloadTooLarge, cause, msg)
}

// decodeError maps a decoding failure to a client-safe error.
func decodeError(err error) error {
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		return tooLarge(mbe.Limit, err)
	}
	var se *jsonv2.SemanticError
	if errors.As(err, &se) {
		return semanticError(err, se)
	}
	var sy *jsontext.SyntacticError
	if errors.As(err, &sy) {
		if errors.Is(sy.Err, io.ErrUnexpectedEOF) {
			return errs.Wrap(errs.CodeInvalidRequest, err, "Request body is truncated JSON.")
		}
		code := FieldCodeMalformedJSON
		if errors.Is(sy.Err, jsontext.ErrDuplicateName) {
			code = FieldCodeDuplicateName
		}
		return withField(err, "Request body is not valid JSON.", errs.FieldError{
			Pointer: string(sy.JSONPointer),
			Code:    code,
			Detail:  fmt.Sprintf("%s (at byte offset %d).", sy.Err.Error(), sy.ByteOffset),
		})
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return errs.Wrap(errs.CodeInvalidRequest, err, "Request body is truncated JSON.")
	}
	return errs.Wrap(errs.CodeInvalidRequest, err, "Request body could not be read.")
}

func semanticError(err error, se *jsonv2.SemanticError) error {
	pointer := string(se.JSONPointer)
	if errors.Is(se.Err, jsonv2.ErrUnknownName) {
		return withField(err, "Request body has an unknown field.", errs.FieldError{
			Pointer: pointer, Code: FieldCodeUnknownField, Detail: "Unknown field.",
		})
	}
	if se.Err != nil {
		// An UnmarshalJSON/UnmarshalText method rejected the value. Only a
		// platform error's message is known to be client-safe.
		detail := "Invalid value."
		var pe *errs.Error
		if errors.As(se.Err, &pe) {
			detail = pe.Message()
		}
		if !isNumberSyntax(se) {
			return withField(err, "Request body has an invalid value.", errs.FieldError{
				Pointer: pointer, Code: FieldCodeInvalidValue, Detail: detail,
			})
		}
	}
	return withField(err, "Request body has a value of the wrong type.", errs.FieldError{
		Pointer: pointer, Code: FieldCodeInvalidType, Detail: "Must be " + jsonTypeName(se.GoType) + ".",
	})
}

// withField returns an invalid_request error with one field error.
func withField(cause error, msg string, fe errs.FieldError) error {
	e := errs.Wrap(errs.CodeInvalidRequest, cause, msg)
	return e.WithFields(fe) //nolint:wrapcheck // WithFields copies a platform error; errs.Wrap already wrapped the cause
}

// isNumberSyntax reports a JSON number that does not fit the Go number type
// (1.5 or 1e400 for an int), which is a type error for the client.
func isNumberSyntax(se *jsonv2.SemanticError) bool {
	return se.JSONKind == '0' && (errors.Is(se.Err, strconv.ErrSyntax) || errors.Is(se.Err, strconv.ErrRange))
}

// jsonTypeName names the JSON type a Go type expects, without exposing Go
// type names.
func jsonTypeName(t reflect.Type) string {
	if t == nil {
		return "a valid value"
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() { //nolint:exhaustive // the remaining kinds have no JSON form
	case reflect.String:
		return "a string"
	case reflect.Bool:
		return "a boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "an integer"
	case reflect.Float32, reflect.Float64:
		return "a number"
	case reflect.Slice, reflect.Array:
		return "an array"
	case reflect.Struct, reflect.Map:
		return "an object"
	default:
		return "a valid value"
	}
}

// WriteJSON writes v as JSON with the given status. The value is encoded
// before anything is written, so an encoding failure becomes a generic 500
// problem (and is returned) instead of a truncated response.
func WriteJSON(w http.ResponseWriter, status int, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		sendProblem(w, genericInternalProblem())
		return errs.Wrap(errs.CodeInternal, err, "encode response")
	}
	body = append(body, '\n')
	h := w.Header()
	h.Set("Content-Type", ContentTypeJSON)
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	if _, err := w.Write(body); err != nil {
		return fmt.Errorf("write response: %w", err)
	}
	return nil
}
