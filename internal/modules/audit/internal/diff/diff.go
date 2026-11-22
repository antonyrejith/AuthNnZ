// Package diff computes field-level differences between two JSON documents
// as RFC 6902-style entries that carry both the before and the after value,
// so either version can be rebuilt from the other.
//
// Rules:
//   - Values are compared in their generic JSON form (Normalize): objects
//     are map[string]any, arrays []any, numbers json.Number.
//   - Objects recurse: a key only in before is "remove", only in after
//     "add", in both with different values "replace" (or recursion when both
//     values are objects).
//   - Arrays are compared as whole values: any change replaces the array.
//     Element-level array patches depend on element identity, which a
//     generic diff cannot know; whole values keep entries exact and simple.
//   - A missing document (nil) on one side counts as an empty object when
//     the other side is an object; otherwise unequal roots are replaced at
//     the root path "".
//   - Entries are ordered by path (object keys sorted), so the output is
//     deterministic.
package diff

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
)

// Operations.
const (
	OpAdd     = "add"
	OpRemove  = "remove"
	OpReplace = "replace"
)

// Entry is one changed value at a JSON Pointer (RFC 6901) path.
type Entry struct {
	Op     string
	Path   string
	Before any // nil for add
	After  any // nil for remove
}

// Normalize converts v to its generic JSON form by marshalling and decoding
// it (numbers as json.Number, so no precision is lost). json.RawMessage and
// []byte holding JSON are decoded directly. nil stays nil.
func Normalize(v any) (any, error) {
	if v == nil {
		return nil, nil //nolint:nilnil // nil is the generic form of a missing document
	}
	var raw []byte
	switch x := v.(type) {
	case json.RawMessage:
		raw = x
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("diff: marshal value: %w", err)
		}
		raw = b
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var out any
	if err := dec.Decode(&out); err != nil {
		return nil, fmt.Errorf("diff: decode value: %w", err)
	}
	return out, nil
}

// Compute returns the entries that turn before into after. Both must be in
// generic JSON form (see Normalize).
func Compute(before, after any) []Entry {
	_, bObj := before.(map[string]any)
	_, aObj := after.(map[string]any)
	if before == nil && aObj {
		before = map[string]any{}
	}
	if after == nil && bObj {
		after = map[string]any{}
	}
	out := []Entry{}
	walk(&out, "", before, after)
	return out
}

func walk(out *[]Entry, path string, before, after any) {
	bm, bObj := before.(map[string]any)
	am, aObj := after.(map[string]any)
	if !bObj || !aObj {
		if !reflect.DeepEqual(before, after) {
			*out = append(*out, Entry{Op: OpReplace, Path: path, Before: before, After: after})
		}
		return
	}
	keys := make([]string, 0, len(bm)+len(am))
	for k := range bm {
		keys = append(keys, k)
	}
	for k := range am {
		if _, ok := bm[k]; !ok {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	for _, k := range keys {
		p := path + "/" + Escape(k)
		bv, inB := bm[k]
		av, inA := am[k]
		switch {
		case !inB:
			*out = append(*out, Entry{Op: OpAdd, Path: p, After: av})
		case !inA:
			*out = append(*out, Entry{Op: OpRemove, Path: p, Before: bv})
		default:
			walk(out, p, bv, av)
		}
	}
}

// Escape encodes one reference token of a JSON Pointer (RFC 6901).
func Escape(token string) string {
	return strings.ReplaceAll(strings.ReplaceAll(token, "~", "~0"), "/", "~1")
}

// Unescape decodes one reference token of a JSON Pointer.
func Unescape(token string) string {
	return strings.ReplaceAll(strings.ReplaceAll(token, "~1", "/"), "~0", "~")
}

// Split returns the decoded tokens of a pointer ("" is the root: no tokens).
func Split(pointer string) ([]string, error) {
	if pointer == "" {
		return nil, nil
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, fmt.Errorf("diff: pointer %q does not start with /", pointer)
	}
	parts := strings.Split(pointer[1:], "/")
	for i, p := range parts {
		parts[i] = Unescape(p)
	}
	return parts, nil
}

// Apply applies entries forward (before to after) to a copy of doc, which
// must be in generic JSON form. A remove of a whole document yields nil. It
// verifies each entry's before value, so a diff applied to the wrong
// document fails.
func Apply(doc any, entries []Entry) (any, error) {
	doc = deepCopy(doc)
	for _, e := range entries {
		var err error
		if doc, err = applyEntry(doc, e); err != nil {
			return nil, err
		}
	}
	return doc, nil
}

func applyEntry(doc any, e Entry) (any, error) {
	tokens, err := Split(e.Path)
	if err != nil {
		return nil, err
	}
	if len(tokens) == 0 {
		if e.Op != OpAdd && !reflect.DeepEqual(doc, e.Before) {
			return nil, fmt.Errorf("diff: root before value does not match")
		}
		return deepCopy(e.After), nil
	}
	if doc == nil {
		doc = map[string]any{}
	}
	parent, err := locate(doc, tokens[:len(tokens)-1])
	if err != nil {
		return nil, err
	}
	last := tokens[len(tokens)-1]
	cur, exists := parent[last]
	switch e.Op {
	case OpAdd:
		if exists {
			return nil, fmt.Errorf("diff: add at %q: value exists", e.Path)
		}
		parent[last] = deepCopy(e.After)
	case OpRemove, OpReplace:
		if !exists || !reflect.DeepEqual(cur, e.Before) {
			return nil, fmt.Errorf("diff: %s at %q: before value does not match", e.Op, e.Path)
		}
		if e.Op == OpRemove {
			delete(parent, last)
		} else {
			parent[last] = deepCopy(e.After)
		}
	default:
		return nil, fmt.Errorf("diff: unknown op %q", e.Op)
	}
	return doc, nil
}

func locate(doc any, tokens []string) (map[string]any, error) {
	cur := doc
	for _, t := range tokens {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("diff: path through a non-object at %q", t)
		}
		cur = m[t]
	}
	m, ok := cur.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("diff: parent is not an object")
	}
	return m, nil
}

func deepCopy(v any) any {
	switch x := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(x))
		for k, val := range x {
			m[k] = deepCopy(val)
		}
		return m
	case []any:
		s := make([]any, len(x))
		for i, val := range x {
			s[i] = deepCopy(val)
		}
		return s
	default:
		return v
	}
}
