package domain

import (
	"reflect"
	"testing"

	"github.com/antonyrejith/portun/internal/platform/errs"
)

func TestSettingCheck(t *testing.T) {
	t.Parallel()
	good := Setting{Key: "a.b", Schema: raw(`{}`), Default: raw(`5`), Merge: MergeStrictest, Order: LowerIsStricter, Policy: PolicyBounded}
	if err := good.Check(); err != nil {
		t.Fatal(err)
	}
	mut := func(f func(*Setting)) Setting {
		s := good
		f(&s)
		return s
	}
	for name, s := range map[string]Setting{
		"bad key":                  mut(func(s *Setting) { s.Key = "A B" }),
		"no schema":                mut(func(s *Setting) { s.Schema = nil }),
		"no default":               mut(func(s *Setting) { s.Default = nil }),
		"default not JSON":         mut(func(s *Setting) { s.Default = raw(`{`) }),
		"unknown merge":            mut(func(s *Setting) { s.Merge = "weird" }),
		"strictest needs order":    mut(func(s *Setting) { s.Order = "" }),
		"order on another rule":    mut(func(s *Setting) { s.Merge, s.Policy = MergeCeiling, PolicyBounded }),
		"strictest on a string":    mut(func(s *Setting) { s.Default = raw(`"x"`) }),
		"intersection on a number": mut(func(s *Setting) { s.Merge, s.Order = MergeIntersection, "" }),
		"union on a number":        mut(func(s *Setting) { s.Merge, s.Order, s.Policy = MergeUnion, "", PolicyAdditive }),
		"unknown policy":           mut(func(s *Setting) { s.Policy = "weird" }),
		"bounded on most specific": mut(func(s *Setting) { s.Merge, s.Order, s.Default = MergeMostSpecific, "", raw(`"x"`) }),
		"additive on strictest":    mut(func(s *Setting) { s.Policy = PolicyAdditive }),
	} {
		if err := s.Check(); !errs.Is(err, errs.CodeInternal) {
			t.Errorf("%s: Check = %v, want an internal error", name, err)
		}
	}
	if !ValidKey("session.idle_timeout_seconds") || ValidKey("Session") || ValidKey("a..b") {
		t.Error("ValidKey")
	}
}

func TestKindCheck(t *testing.T) {
	t.Parallel()
	good := Kind{Name: "flow.login", Schema: raw(`{}`), CELFields: []CELField{
		{Pointer: "/steps/*/when", Type: CELBool, Vars: []CELVar{{Name: "risk", Type: CELInt}}},
	}}
	if err := good.Check(); err != nil {
		t.Fatal(err)
	}
	mut := func(f func(*Kind)) Kind {
		k := good
		k.CELFields = append([]CELField(nil), good.CELFields...)
		f(&k)
		return k
	}
	for name, k := range map[string]Kind{
		"bad name":     mut(func(k *Kind) { k.Name = "Flow" }),
		"no schema":    mut(func(k *Kind) { k.Schema = nil }),
		"bad pointer":  mut(func(k *Kind) { k.CELFields[0].Pointer = "steps" }),
		"root pointer": mut(func(k *Kind) { k.CELFields[0].Pointer = "" }),
		"duplicate":    mut(func(k *Kind) { k.CELFields = append(k.CELFields, k.CELFields[0]) }),
		"bad type":     mut(func(k *Kind) { k.CELFields[0].Type = "set" }),
		"bad var name": mut(func(k *Kind) { k.CELFields[0].Vars = []CELVar{{Name: "1x", Type: CELInt}} }),
		"bad var type": mut(func(k *Kind) { k.CELFields[0].Vars = []CELVar{{Name: "x", Type: "set"}} }),
		"dup var": mut(func(k *Kind) {
			k.CELFields[0].Vars = []CELVar{{Name: "x", Type: CELInt}, {Name: "x", Type: CELInt}}
		}),
	} {
		if err := k.Check(); !errs.Is(err, errs.CodeInternal) {
			t.Errorf("%s: Check = %v, want an internal error", name, err)
		}
	}
	if !ValidObjectName("login-flow.v2") || ValidObjectName("-x") || ValidObjectName("") || ValidObjectName("a b") {
		t.Error("ValidObjectName")
	}
}

func TestEnumsAndLevels(t *testing.T) {
	t.Parallel()
	if !LevelTenant.Valid() || Level("org").Valid() || len(Levels()) != 3 || Levels()[0] != LevelPlatform {
		t.Error("levels")
	}
	if !ModeOwn.Valid() || Mode("template").Valid() {
		t.Error("modes")
	}
	if ImplicitMode(LevelTenant) != ModeDefault || ImplicitMode(LevelEnvironment) != ModeParent {
		t.Error("implicit modes")
	}
	if !PolicyLocked.Valid() || Policy("x").Valid() || !MergeUnion.Valid() || MergeRule("x").Valid() {
		t.Error("policies and rules")
	}
	for _, ct := range []CELType{CELBool, CELString, CELInt, CELDouble, CELList, CELMap} {
		if !ct.Valid() {
			t.Errorf("%s is invalid", ct)
		}
	}
	r := SettingRow{}
	if r.Level() != LevelTenant {
		t.Error("a row without an environment is tenant level")
	}
}

func TestPointers(t *testing.T) {
	t.Parallel()
	doc, err := Decode(raw(`{"steps":[{"when":"a"},{"when":"b"},{}],"m":{"x":{"when":"c"},"y":{"when":"d"}},"a/b":{"c~d":1}}`))
	if err != nil {
		t.Fatal(err)
	}
	for pattern, want := range map[string][]string{
		"/steps/*/when": {"/steps/0/when", "/steps/1/when"},
		"/steps/1/when": {"/steps/1/when"},
		"/m/*/when":     {"/m/x/when", "/m/y/when"},
		"/a~1b/c~0d":    {"/a~1b/c~0d"},
		"/steps/9/when": nil,
		"/none":         nil,
		"/steps/x":      nil,
	} {
		got, err := Expand(doc, pattern)
		if err != nil {
			t.Fatalf("%s: %v", pattern, err)
		}
		var ptrs []string
		for _, g := range got {
			ptrs = append(ptrs, g.Pointer)
		}
		if !reflect.DeepEqual(ptrs, want) {
			t.Errorf("%s = %v, want %v", pattern, ptrs, want)
		}
	}
	if _, err := Expand(doc, "steps"); err == nil {
		t.Error("a pattern without a leading slash was accepted")
	}
	if _, err := Decode(raw(`{`)); !errs.Is(err, errs.CodeValidationFailed) {
		t.Errorf("Decode = %v", err)
	}
	toks, err := SplitPointer("/a~1b/c~0d")
	if err != nil || !reflect.DeepEqual(toks, []string{"a/b", "c~d"}) {
		t.Errorf("SplitPointer = %v, %v", toks, err)
	}
}

func TestDiff(t *testing.T) {
	t.Parallel()
	got, err := Diff(raw(`{"a":1,"b":[1,2,3],"c":{"d":"x"},"e":true}`), raw(`{"a":2,"b":[1,5],"c":{"d":"x","f":1},"g":null}`))
	if err != nil {
		t.Fatal(err)
	}
	type e struct{ Op, Path string }
	var ops []e
	for _, d := range got {
		ops = append(ops, e{d.Op, d.Path})
	}
	want := []e{{"replace", "/a"}, {"replace", "/b/1"}, {"remove", "/b/2"}, {"add", "/c/f"}, {"remove", "/e"}, {"add", "/g"}}
	if !reflect.DeepEqual(ops, want) {
		t.Fatalf("ops = %v, want %v", ops, want)
	}
	if got, err := Diff(raw(`{"a":[1]}`), raw(`{"a":[1,2]}`)); err != nil || len(got) != 1 || got[0].Op != "add" || got[0].Path != "/a/1" {
		t.Errorf("array growth = %+v, %v", got, err)
	}
	if got, err := Diff(raw(`1`), raw(`"x"`)); err != nil || len(got) != 1 || got[0].Path != "" || got[0].Op != "replace" {
		t.Errorf("root replace = %+v, %v", got, err)
	}
	if got, err := Diff(raw(`{"a":1}`), raw(`{"a":1}`)); err != nil || len(got) != 0 {
		t.Errorf("equal documents = %+v, %v", got, err)
	}
	if _, err := Diff(raw(`{`), raw(`1`)); err == nil {
		t.Error("invalid before accepted")
	}
	if _, err := Diff(raw(`1`), raw(`{`)); err == nil {
		t.Error("invalid after accepted")
	}
	if !Equal(raw(`{"a":1,"b":2}`), raw(`{ "b":2, "a":1 }`)) || Equal(raw(`1`), raw(`2`)) || Equal(raw(`{`), raw(`{`)) != true || Equal(raw(`{`), raw(`1`)) {
		t.Error("Equal")
	}
}
