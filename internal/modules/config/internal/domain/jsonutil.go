package domain

import (
	"bytes"
	"encoding/json"
	"reflect"
)

// decode parses raw JSON into the generic form; ok is false for invalid JSON.
func decode(raw json.RawMessage) (any, bool) {
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&v); err != nil {
		return nil, false
	}
	return v, true
}

func asNumber(raw json.RawMessage) (float64, bool) {
	v, ok := decode(raw)
	if !ok {
		return 0, false
	}
	f, ok := v.(float64)
	return f, ok
}

func asStrings(raw json.RawMessage) ([]string, bool) {
	v, ok := decode(raw)
	if !ok {
		return nil, false
	}
	arr, ok := v.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		s, ok := e.(string)
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

// Equal reports whether two JSON documents are equal as values.
func Equal(a, b json.RawMessage) bool {
	av, aok := decode(a)
	bv, bok := decode(b)
	if !aok || !bok {
		return aok == bok && bytes.Equal(a, b)
	}
	return reflect.DeepEqual(av, bv)
}
