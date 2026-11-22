package domain

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/ids"
)

// CELType is the type of a CEL variable or result.
type CELType string

// CEL types.
const (
	CELBool   CELType = "bool"
	CELString CELType = "string"
	CELInt    CELType = "int"
	CELDouble CELType = "double"
	CELList   CELType = "list"
	CELMap    CELType = "map"
)

// Valid reports whether t is a known type.
func (t CELType) Valid() bool {
	switch t {
	case CELBool, CELString, CELInt, CELDouble, CELList, CELMap:
		return true
	default:
		return false
	}
}

// CELVar declares a variable an expression may use.
type CELVar struct {
	Name string
	Type CELType
}

// CELField says that the spec holds a CEL expression at Pointer. The pointer
// is an RFC 6901 JSON Pointer in which a "*" token stands for every item of
// an array (or every member of an object).
type CELField struct {
	Pointer string
	// Type is the type the expression must return.
	Type CELType
	Vars []CELVar
}

// Kind is the definition of a kind of configuration object: the JSON Schema
// 2020-12 document its specs must satisfy and the CEL expressions they hold.
type Kind struct {
	Name      string
	Schema    []byte
	CELFields []CELField
}

var (
	kindPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)*$`)
	namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)
	varPattern  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// ValidKindName reports whether name can name a kind.
func ValidKindName(name string) bool { return len(name) <= 64 && kindPattern.MatchString(name) }

// ValidObjectName reports whether name can name an object.
func ValidObjectName(name string) bool { return namePattern.MatchString(name) }

// Check validates the definition apart from the schema, which the
// application layer compiles. Errors are errs.CodeInternal: a kind comes from
// code.
func (k Kind) Check() error {
	fail := func(msg string) error { return errs.New(errs.CodeInternal, "config: kind "+k.Name+": "+msg) }
	if !ValidKindName(k.Name) {
		return errs.New(errs.CodeInternal, "config: kind name "+quote(k.Name)+" is not dotted lowercase")
	}
	if len(k.Schema) == 0 {
		return fail("a schema is required")
	}
	seen := map[string]bool{}
	for _, f := range k.CELFields {
		if _, err := SplitPointer(f.Pointer); err != nil || f.Pointer == "" {
			return fail("CEL field pointer " + quote(f.Pointer) + " is not a JSON Pointer")
		}
		if seen[f.Pointer] {
			return fail("CEL field " + quote(f.Pointer) + " is declared twice")
		}
		seen[f.Pointer] = true
		if !f.Type.Valid() {
			return fail("CEL field " + quote(f.Pointer) + " has an unknown result type")
		}
		names := map[string]bool{}
		for _, v := range f.Vars {
			if !varPattern.MatchString(v.Name) || !v.Type.Valid() || names[v.Name] {
				return fail("CEL field " + quote(f.Pointer) + " has an invalid or duplicate variable " + quote(v.Name))
			}
			names[v.Name] = true
		}
	}
	return nil
}

// Object statuses.
const (
	ObjectActive   = "active"
	ObjectArchived = "archived"
)

// Version statuses.
const (
	VersionDraft     = "draft"
	VersionPublished = "published"
	VersionArchived  = "archived"
)

// Object is a configuration object of one environment.
type Object struct {
	ID            ids.ConfigObjectID
	EnvironmentID ids.EnvironmentID
	Kind          string
	Name          string
	Status        string
	// CurrentVersion is the published version in force; 0 if none.
	CurrentVersion int
	// Version is the object's etag, for optimistic concurrency.
	Version   int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Version is one version of an object's spec.
type Version struct {
	ObjectID    ids.ConfigObjectID
	Version     int
	Status      string
	Spec        json.RawMessage
	Checksum    string
	ChangeNote  string
	CreatedBy   string
	CreatedAt   time.Time
	PublishedAt *time.Time
}

// PointerValue is a value found at a concrete JSON Pointer of a document.
type PointerValue struct {
	Pointer string
	Value   any
}

// SplitPointer splits an RFC 6901 pointer into its unescaped tokens. The
// empty pointer is the whole document (no tokens).
func SplitPointer(p string) ([]string, error) {
	if p == "" {
		return nil, nil
	}
	if !strings.HasPrefix(p, "/") {
		return nil, errs.New(errs.CodeInvalidRequest, "a JSON Pointer starts with /")
	}
	parts := strings.Split(p[1:], "/")
	for i, t := range parts {
		parts[i] = strings.ReplaceAll(strings.ReplaceAll(t, "~1", "/"), "~0", "~")
	}
	return parts, nil
}

// EscapeToken escapes one pointer token (RFC 6901).
func EscapeToken(t string) string {
	return strings.ReplaceAll(strings.ReplaceAll(t, "~", "~0"), "/", "~1")
}

// Expand finds the values a pointer pattern selects in a decoded JSON
// document. A "*" token selects every item of an array and every member of
// an object, so one pattern yields several concrete pointers; a pattern that
// matches nothing yields none.
func Expand(doc any, pattern string) ([]PointerValue, error) {
	tokens, err := SplitPointer(pattern)
	if err != nil {
		return nil, err
	}
	found := []PointerValue{{Pointer: "", Value: doc}}
	for _, t := range tokens {
		var next []PointerValue
		for _, f := range found {
			next = append(next, step(f, t)...)
		}
		found = next
	}
	return found, nil
}

func step(f PointerValue, token string) []PointerValue {
	switch v := f.Value.(type) {
	case map[string]any:
		if token == "*" {
			out := make([]PointerValue, 0, len(v))
			for _, k := range sortedKeys(v) {
				out = append(out, PointerValue{Pointer: f.Pointer + "/" + EscapeToken(k), Value: v[k]})
			}
			return out
		}
		if c, ok := v[token]; ok {
			return []PointerValue{{Pointer: f.Pointer + "/" + EscapeToken(token), Value: c}}
		}
	case []any:
		if token == "*" {
			out := make([]PointerValue, 0, len(v))
			for i, c := range v {
				out = append(out, PointerValue{Pointer: f.Pointer + "/" + strconv.Itoa(i), Value: c})
			}
			return out
		}
		if i, err := strconv.Atoi(token); err == nil && i >= 0 && i < len(v) {
			return []PointerValue{{Pointer: f.Pointer + "/" + token, Value: v[i]}}
		}
	}
	return nil
}

// Decode parses a JSON document into the generic form used by Expand.
func Decode(raw json.RawMessage) (any, error) {
	v, ok := decode(raw)
	if !ok {
		return nil, errs.New(errs.CodeValidationFailed, "the document is not valid JSON")
	}
	return v, nil
}
