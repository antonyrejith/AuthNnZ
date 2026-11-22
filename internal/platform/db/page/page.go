// Package page implements keyset pagination for list APIs (ADR-0009):
// page_size, page_token and next_page_token.
//
// A list query orders its rows by a unique key (for example created_at, id)
// and fetches the rows after the last row of the previous page. The page
// token carries that row's key values. Tokens are opaque to clients:
// base64url (no padding) of a small versioned JSON document holding the key
// values as strings and a short hash of the query binding. The binding is a
// string the caller derives from everything that shapes the result (the
// resource, filters and order), so a token taken from one query is rejected
// by another. Tokens are not encrypted or signed: they only position a query
// the caller is allowed to run anyway.
//
// Typical use:
//
//	binding := "users|status=" + status + "|order=created_at"
//	p, err := page.Parse(req, binding, 2)
//	// SELECT ... WHERE (created_at, id) > ($1, $2) ... LIMIT p.Fetch()
//	rows, next := page.Trim(p, rows, binding, func(u User) []string {
//		return []string{u.CreatedAt.Format(time.RFC3339Nano), u.ID.String()}
//	})
package page

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strconv"

	"github.com/antonyrejith/portun/internal/platform/errs"
)

// Page size limits (ADR-0009).
const (
	DefaultSize = 50
	MinSize     = 1
	MaxSize     = 500
)

const (
	tokenVersion = 1
	// maxTokenLen bounds the work spent on a client-supplied token.
	maxTokenLen = 2048
	// maxKeys bounds the number of key values in a token.
	maxKeys = 8
	// bindingHashLen is the number of SHA-256 bytes kept in a token.
	bindingHashLen = 8
)

// JSON pointers of the request fields, for field errors.
const (
	pointerSize  = "/page_size"
	pointerToken = "/page_token"
)

// Request is the paging input of a list request.
type Request struct {
	// Size is page_size; 0 means DefaultSize.
	Size int
	// Token is page_token; empty for the first page.
	Token string
}

// Page is a validated request.
type Page struct {
	// Size is the number of rows to return, MinSize to MaxSize.
	Size int
	// After holds the key values of the last row of the previous page, in
	// query order; nil for the first page.
	After []string
}

// First reports whether this is the first page.
func (p Page) First() bool { return p.After == nil }

// Fetch is the LIMIT to query with: one row more than Size, so that Trim can
// tell whether another page follows.
func (p Page) Fetch() int { return p.Size + 1 }

// Parse validates r for the query identified by binding, whose key has keys
// values. Problems with both fields are reported together, as
// CodeInvalidRequest with field errors at /page_size and /page_token.
func Parse(r Request, binding string, keys int) (Page, error) {
	var fields []errs.FieldError
	size := r.Size
	switch {
	case size == 0:
		size = DefaultSize
	case size < MinSize || size > MaxSize:
		fields = append(fields, errs.FieldError{
			Pointer: pointerSize,
			Code:    "out_of_range",
			Detail:  "page_size must be between " + strconv.Itoa(MinSize) + " and " + strconv.Itoa(MaxSize),
		})
	}
	var after []string
	if r.Token != "" {
		k, err := decode(r.Token, binding)
		switch {
		case err != nil:
			fields = append(fields, tokenField(err.Error()))
		case len(k) != keys:
			fields = append(fields, tokenField("the token does not fit this query"))
		default:
			after = k
		}
	}
	if len(fields) > 0 {
		return Page{}, errs.New(errs.CodeInvalidRequest, "invalid paging parameters").WithFields(fields...) //nolint:wrapcheck // errs constructor
	}
	return Page{Size: size, After: after}, nil
}

// Trim cuts rows fetched with LIMIT p.Fetch() to the page size and returns
// the token of the next page, or "" when this is the last page. key returns
// a row's key values in query order.
func Trim[T any](p Page, rows []T, binding string, key func(T) []string) ([]T, string) {
	if len(rows) <= p.Size {
		return rows, ""
	}
	rows = rows[:p.Size]
	return rows, Encode(binding, key(rows[len(rows)-1]))
}

// payload is the JSON document inside a token.
type payload struct {
	Version int      `json:"v"`
	Query   string   `json:"q"`
	Keys    []string `json:"k"`
}

// Encode returns the token for the page after the row with the given key
// values, for the query identified by binding.
func Encode(binding string, keys []string) string {
	// Marshal cannot fail for this type (strings are coerced to valid UTF-8).
	b, _ := json.Marshal(payload{Version: tokenVersion, Query: bindingHash(binding), Keys: keys})
	return base64.RawURLEncoding.EncodeToString(b)
}

// Decode returns the key values of token. It fails with CodeInvalidRequest
// (field /page_token) if the token is malformed, has an unknown version, or
// was issued for a different binding.
func Decode(token, binding string) ([]string, error) {
	keys, err := decode(token, binding)
	if err != nil {
		return nil, errs.New(errs.CodeInvalidRequest, "invalid page_token").WithFields(tokenField(err.Error())) //nolint:wrapcheck // errs constructor
	}
	return keys, nil
}

// decode returns a plain error whose text is a client-safe field detail.
func decode(token, binding string) ([]string, error) {
	if token == "" || len(token) > maxTokenLen {
		return nil, errors.New("the token is malformed")
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil {
		return nil, errors.New("the token is malformed")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var p payload
	if err := dec.Decode(&p); err != nil {
		return nil, errors.New("the token is malformed")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("the token is malformed")
	}
	if p.Version != tokenVersion {
		return nil, errors.New("the token version is not supported")
	}
	if p.Query != bindingHash(binding) {
		return nil, errors.New("the token belongs to a different query")
	}
	if len(p.Keys) == 0 || len(p.Keys) > maxKeys {
		return nil, errors.New("the token is malformed")
	}
	return p.Keys, nil
}

func tokenField(detail string) errs.FieldError {
	return errs.FieldError{Pointer: pointerToken, Code: "invalid", Detail: detail}
}

// bindingHash is the first bytes of SHA-256(binding), base64url-encoded.
func bindingHash(binding string) string {
	sum := sha256.Sum256([]byte(binding))
	return base64.RawURLEncoding.EncodeToString(sum[:bindingHashLen])
}
