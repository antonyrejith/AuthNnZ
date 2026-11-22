package app

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/antonyrejith/portun/internal/modules/config/internal/domain"
	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/ids"
)

func newID[K ids.Kind](t *testing.T) ids.ID[K] {
	t.Helper()
	id, err := ids.New[K]()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func flowKind() domain.Kind {
	return domain.Kind{
		Name: "flow",
		Schema: []byte(`{"type":"object","required":["when","tags","who"],"additionalProperties":false,"properties":{
			"when":{"type":"string"},"tags":{"type":"string"},"who":{"type":"string"},"n":{"type":"string"},"ratio":{"type":"string"}}}`),
		CELFields: []domain.CELField{
			{Pointer: "/when", Type: domain.CELBool, Vars: []domain.CELVar{{Name: "x", Type: domain.CELInt}}},
			{Pointer: "/tags", Type: domain.CELList, Vars: []domain.CELVar{{Name: "x", Type: domain.CELInt}}},
			{Pointer: "/who", Type: domain.CELMap, Vars: []domain.CELVar{{Name: "x", Type: domain.CELInt}}},
			{Pointer: "/n", Type: domain.CELInt, Vars: []domain.CELVar{{Name: "x", Type: domain.CELInt}}},
			{Pointer: "/ratio", Type: domain.CELDouble, Vars: []domain.CELVar{{Name: "x", Type: domain.CELInt}}},
		},
	}
}

// TestRegisterKind covers the checks at registration.
//
// Covers: DYN-05
func TestRegisterKind(t *testing.T) {
	t.Parallel()
	r := NewRegistry()
	if err := r.RegisterKind(flowKind()); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterKind(flowKind()); !errs.Is(err, errs.CodeInternal) {
		t.Errorf("duplicate kind: %v", err)
	}
	for name, k := range map[string]domain.Kind{
		"bad name":        {Name: "Bad", Schema: []byte(`{}`)},
		"bad schema":      {Name: "a", Schema: []byte(`{"type": 5}`)},
		"schema not JSON": {Name: "b", Schema: []byte(`{`)},
	} {
		if err := r.RegisterKind(k); !errs.Is(err, errs.CodeInternal) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if got := r.KindNames(); len(got) != 1 || got[0] != "flow" {
		t.Errorf("KindNames = %v", got)
	}
}

// TestRegisterSetting covers the consistency checks at registration.
//
// Covers: TEN-06
func TestRegisterSetting(t *testing.T) {
	t.Parallel()
	r := NewRegistry()
	ok := domain.Setting{Key: "a.b", Schema: []byte(`{"type":"integer"}`), Default: json.RawMessage(`5`), Merge: domain.MergeCeiling, Policy: domain.PolicyBounded}
	if err := r.RegisterSetting(ok); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterSetting(ok); !errs.Is(err, errs.CodeInternal) {
		t.Errorf("duplicate: %v", err)
	}
	mut := func(f func(*domain.Setting)) domain.Setting {
		s := ok
		s.Key = "c.d"
		f(&s)
		return s
	}
	for name, s := range map[string]domain.Setting{
		"default fails the schema": mut(func(s *domain.Setting) { s.Default = json.RawMessage(`"five"`) }),
		"intersection on a number": mut(func(s *domain.Setting) { s.Merge, s.Policy = domain.MergeIntersection, domain.PolicyOverridable }),
		"bad schema":               mut(func(s *domain.Setting) { s.Schema = []byte(`{"type":9}`) }),
		"bad key":                  mut(func(s *domain.Setting) { s.Key = "X" }),
	} {
		if err := r.RegisterSetting(s); !errs.Is(err, errs.CodeInternal) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if got := r.SettingKeys(); len(got) != 1 || got[0] != "a.b" {
		t.Errorf("SettingKeys = %v", got)
	}
}

func TestCheckSpecAndEval(t *testing.T) {
	t.Parallel()
	r := NewRegistry()
	if err := r.RegisterKind(flowKind()); err != nil {
		t.Fatal(err)
	}
	k, _ := r.kind("flow")
	spec := json.RawMessage(`{"when":"x > 1","tags":"[x, x + 1]","who":"{'a': x}","n":"x * 2","ratio":"1.5 * double(x)"}`)
	if issues := k.checkSpec(spec); len(issues) != 0 {
		t.Fatalf("issues = %+v", issues)
	}
	for name, tc := range map[string]struct {
		spec    string
		pointer string
	}{
		"wrong result type": {`{"when":"x + 1","tags":"[x]","who":"{}","n":"1","ratio":"1.0"}`, "/when"},
		"syntax error":      {`{"when":"x >","tags":"[x]","who":"{}","n":"1","ratio":"1.0"}`, "/when"},
		"unknown variable":  {`{"when":"y > 1","tags":"[x]","who":"{}","n":"1","ratio":"1.0"}`, "/when"},
		"too long":          {`{"when":"` + strings.Repeat("1 + ", 2000) + `1 > 0","tags":"[x]","who":"{}","n":"1","ratio":"1.0"}`, "/when"},
		"schema violation":  {`{"when":5,"tags":"[x]","who":"{}"}`, "/when"},
		"missing required":  {`{"when":"true"}`, ""},
	} {
		issues := k.checkSpec(json.RawMessage(tc.spec))
		if len(issues) == 0 {
			t.Errorf("%s: no issues", name)
			continue
		}
		if tc.pointer != "" && issues[0].Pointer != tc.pointer {
			t.Errorf("%s: pointer = %q, want %q (%+v)", name, issues[0].Pointer, tc.pointer, issues)
		}
	}
	if issues := validateAgainst(k.schema, []byte(`{`)); len(issues) != 1 || issues[0].Code != "invalid_json" {
		t.Errorf("invalid JSON = %+v", issues)
	}

	// Build a bundle from a payload and evaluate every result type.
	tenant, env := newID[ids.TenantKind](t), newID[ids.EnvironmentKind](t)
	raw, sum, err := encodePayload(tenant, env, 3, map[string]json.RawMessage{"a.b": json.RawMessage(`1`)},
		map[string]map[string]json.RawMessage{"flow": {"one": spec}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := r.buildBundle(raw, sum)
	if err != nil {
		t.Fatal(err)
	}
	if b.Version != 3 || b.TenantID != tenant || b.EnvironmentID != env || b.Checksum != sum {
		t.Fatalf("bundle = %+v", b)
	}
	vars := map[string]any{"x": int64(4)}
	for ptr, want := range map[string]any{"/when": true, "/n": int64(8), "/ratio": 6.0} {
		if got, err := b.Eval("flow", "one", ptr, vars); err != nil || got != want {
			t.Errorf("Eval %s = %v, %v; want %v", ptr, got, err, want)
		}
	}
	if got, err := b.Eval("flow", "one", "/tags", vars); err != nil {
		t.Errorf("list: %v", err)
	} else if l, ok := got.([]any); !ok || len(l) != 2 {
		t.Errorf("list = %#v", got)
	}
	if got, err := b.Eval("flow", "one", "/who", vars); err != nil {
		t.Errorf("map: %v", err)
	} else if m, ok := got.(map[string]any); !ok || len(m) != 1 {
		t.Errorf("map = %#v", got)
	}
	if _, err := b.Eval("flow", "none", "/when", vars); !errs.Is(err, errs.CodeNotFound) {
		t.Errorf("unknown object: %v", err)
	}

	// A tampered payload is rejected.
	if _, err := r.buildBundle(append([]byte(nil), []byte(strings.Replace(string(raw), `"version":3`, `"version":4`, 1))...), sum); !errs.Is(err, errs.CodeInternal) {
		t.Errorf("tampered bundle: %v", err)
	}
	if _, err := r.buildBundle(json.RawMessage(`{`), sum); err == nil {
		t.Error("invalid payload accepted")
	}
	// A payload whose objects no longer compile (the kind changed) is rejected.
	r2 := NewRegistry()
	k2 := flowKind()
	k2.CELFields = k2.CELFields[:1]
	k2.CELFields[0].Type = domain.CELString
	if err := r2.RegisterKind(k2); err != nil {
		t.Fatal(err)
	}
	if _, err := r2.buildBundle(raw, sum); !errs.Is(err, errs.CodeInternal) {
		t.Errorf("incompatible kind: %v", err)
	}
	// An object of an unregistered kind is kept without programs.
	if b, err := NewRegistry().buildBundle(raw, sum); err != nil || len(b.programs) != 0 || len(b.Objects["flow"]) != 1 {
		t.Errorf("unregistered kind: %v", err)
	}
}

func TestChecksum(t *testing.T) {
	t.Parallel()
	a, err := Checksum([]byte(`{"b":1,"a":[1, 2]}`))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Checksum([]byte(`{ "a":[1,2], "b":1 }`))
	if err != nil || a != b || !strings.HasPrefix(a, "sha256:") || len(a) != len("sha256:")+64 {
		t.Fatalf("checksums = %q, %q, %v", a, b, err)
	}
	if _, err := Checksum([]byte(`{`)); !errs.Is(err, errs.CodeInternal) {
		t.Errorf("invalid JSON: %v", err)
	}
}

func TestBundleCache(t *testing.T) {
	t.Parallel()
	c := newBundleCache(2)
	keys := make([]cacheKey, 3)
	for i := range keys {
		keys[i] = cacheKey{newID[ids.TenantKind](t), newID[ids.EnvironmentKind](t)}
	}
	e0 := c.get(keys[0])
	c.get(keys[1])
	if c.get(keys[0]) != e0 {
		t.Fatal("a cached key returned a new entry")
	}
	c.get(keys[2]) // evicts keys[1], the least recently used
	if c.peek(keys[1]) != nil || c.peek(keys[0]) == nil || c.peek(keys[2]) == nil || len(c.keys()) != 2 {
		t.Fatalf("eviction: %d entries", len(c.keys()))
	}
	e0.swapIfNewer(&Bundle{Version: 2})
	e0.swapIfNewer(&Bundle{Version: 1})
	e0.swapIfNewer(&Bundle{Version: 2})
	if got := e0.ptr.Load().Version; got != 2 {
		t.Fatalf("version = %d, want 2 (older and equal bundles do not replace)", got)
	}
	e0.swapIfNewer(&Bundle{Version: 3})
	if got := e0.ptr.Load().Version; got != 3 {
		t.Fatalf("version = %d, want 3", got)
	}
}

func TestMatchPattern(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		pattern, key string
		want         bool
	}{
		{"*", "a.b", true}, {"a.*", "a.b", true}, {"a.*", "ab.c", false}, {"a.b", "a.b", true}, {"a.b", "a.bc", false},
	} {
		if got := matchPattern(tc.pattern, tc.key); got != tc.want {
			t.Errorf("matchPattern(%q, %q) = %v", tc.pattern, tc.key, got)
		}
	}
}

func TestNewRequiresDependencies(t *testing.T) {
	t.Parallel()
	if _, err := New(Deps{}); !errs.Is(err, errs.CodeInternal) {
		t.Errorf("New = %v", err)
	}
}
