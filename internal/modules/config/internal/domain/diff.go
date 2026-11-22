package domain

import (
	"encoding/json"
	"reflect"
	"slices"
	"strconv"
)

// DiffEntry is one changed field between two JSON documents: an RFC 6902
// style operation (add, remove, replace) at a JSON Pointer with both values.
type DiffEntry struct {
	Op     string
	Path   string
	Before any
	After  any
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// Diff returns the field-level difference between two JSON documents. Objects
// are compared member by member, arrays item by item (an array that grows or
// shrinks adds or removes its tail), anything else is replaced as a whole.
func Diff(before, after json.RawMessage) ([]DiffEntry, error) {
	b, err := Decode(before)
	if err != nil {
		return nil, err
	}
	a, err := Decode(after)
	if err != nil {
		return nil, err
	}
	var out []DiffEntry
	walk(&out, "", b, a)
	return out, nil
}

func walk(out *[]DiffEntry, path string, before, after any) {
	switch b := before.(type) {
	case map[string]any:
		if a, ok := after.(map[string]any); ok {
			walkMap(out, path, b, a)
			return
		}
	case []any:
		if a, ok := after.([]any); ok {
			walkSlice(out, path, b, a)
			return
		}
	}
	if !reflect.DeepEqual(before, after) {
		*out = append(*out, DiffEntry{Op: "replace", Path: path, Before: before, After: after})
	}
}

func walkMap(out *[]DiffEntry, path string, b, a map[string]any) {
	keys := sortedKeys(b)
	for _, k := range sortedKeys(a) {
		if _, ok := b[k]; !ok {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	for _, k := range keys {
		p := path + "/" + EscapeToken(k)
		bv, inB := b[k]
		av, inA := a[k]
		switch {
		case inB && inA:
			walk(out, p, bv, av)
		case inA:
			*out = append(*out, DiffEntry{Op: "add", Path: p, After: av})
		default:
			*out = append(*out, DiffEntry{Op: "remove", Path: p, Before: bv})
		}
	}
}

func walkSlice(out *[]DiffEntry, path string, b, a []any) {
	for i := range max(len(b), len(a)) {
		p := path + "/" + strconv.Itoa(i)
		switch {
		case i < len(b) && i < len(a):
			walk(out, p, b[i], a[i])
		case i < len(a):
			*out = append(*out, DiffEntry{Op: "add", Path: p, After: a[i]})
		default:
			*out = append(*out, DiffEntry{Op: "remove", Path: p, Before: b[i]})
		}
	}
}
