package domain

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/antonyrejith/portun/internal/platform/errs"
)

func raw(s string) json.RawMessage { return json.RawMessage(s) }

func def(merge MergeRule, order Order, policy Policy, dflt string) Setting {
	return Setting{Key: "x.y", Schema: raw(`{}`), Default: raw(dflt), Merge: merge, Order: order, Policy: policy}
}

func own(l Level, v string) LevelEntry {
	return LevelEntry{Level: l, Exists: true, Mode: ModeOwn, Value: raw(v)}
}

func inMode(l Level, m Mode) LevelEntry {
	return LevelEntry{Level: l, Exists: true, Mode: m}
}

func values(rs []Resolved) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = string(r.Value)
	}
	return out
}

// TestResolve checks the merge rules and modes along the path.
//
// Covers: TEN-06
func TestResolve(t *testing.T) {
	t.Parallel()
	const (
		tenant = LevelTenant
		env    = LevelEnvironment
	)
	strictLow := def(MergeStrictest, LowerIsStricter, PolicyBounded, `1800`)
	strictHigh := def(MergeStrictest, HigherIsStricter, PolicyBounded, `365`)
	methods := def(MergeIntersection, "", PolicyBounded, `["a","b","c"]`)
	roles := def(MergeUnion, "", PolicyAdditive, `[]`)
	quota := def(MergeCeiling, "", PolicyBounded, `100`)
	name := def(MergeMostSpecific, "", PolicyOverridable, `"Portun"`)
	locked := def(MergeMostSpecific, "", PolicyLocked, `"fixed"`)

	tests := []struct {
		name     string
		setting  Setting
		platform PlatformEntry
		lower    []LevelEntry
		want     []string // effective values: platform, tenant, environment
		source   Level
		mode     Mode
	}{
		{"untouched is the default", strictLow, PlatformEntry{}, []LevelEntry{{Level: tenant}, {Level: env}}, []string{"1800", "1800", "1800"}, LevelPlatform, ModeParent},
		{"strictest lower: shorter wins", strictLow, PlatformEntry{}, []LevelEntry{own(tenant, "900"), own(env, "1200")}, []string{"1800", "900", "900"}, LevelTenant, ModeOwn},
		{"strictest lower: child tightens", strictLow, PlatformEntry{}, []LevelEntry{own(tenant, "900"), own(env, "300")}, []string{"1800", "900", "300"}, LevelEnvironment, ModeOwn},
		{"strictest higher: longer wins", strictHigh, PlatformEntry{}, []LevelEntry{own(tenant, "400"), own(env, "30")}, []string{"365", "400", "400"}, LevelTenant, ModeOwn},
		{"platform value beats a weaker default", strictLow, PlatformEntry{Value: raw(`600`), Version: 4}, []LevelEntry{{Level: tenant}, inMode(env, ModeDefault)}, []string{"600", "600", "600"}, LevelPlatform, ModeDefault},
		{"intersection", methods, PlatformEntry{}, []LevelEntry{own(tenant, `["c","a","z"]`), own(env, `["a"]`)}, []string{`["a","b","c"]`, `["a","c"]`, `["a"]`}, LevelEnvironment, ModeOwn},
		{"union", roles, PlatformEntry{}, []LevelEntry{own(tenant, `["x"]`), own(env, `["y","x"]`)}, []string{`[]`, `["x"]`, `["x","y"]`}, LevelEnvironment, ModeOwn},
		{"ceiling", quota, PlatformEntry{}, []LevelEntry{own(tenant, "50"), own(env, "80")}, []string{"100", "50", "50"}, LevelTenant, ModeOwn},
		{"most specific", name, PlatformEntry{}, []LevelEntry{own(tenant, `"Acme"`), {Level: env}}, []string{`"Portun"`, `"Acme"`, `"Acme"`}, LevelTenant, ModeParent},
		{"default mode ignores the parent's choice", name, PlatformEntry{}, []LevelEntry{own(tenant, `"Acme"`), inMode(env, ModeDefault)}, []string{`"Portun"`, `"Acme"`, `"Portun"`}, LevelEnvironment, ModeDefault},
		{"parent mode follows", name, PlatformEntry{}, []LevelEntry{own(tenant, `"Acme"`), inMode(env, ModeParent)}, []string{`"Portun"`, `"Acme"`, `"Acme"`}, LevelTenant, ModeParent},
		{"locked ignores saved modes", locked, PlatformEntry{Value: raw(`"set"`)}, []LevelEntry{own(tenant, `"other"`), own(env, `"more"`)}, []string{`"set"`, `"set"`, `"set"`}, LevelPlatform, ModeParent},
		{"platform policy replaces the definition's", name, PlatformEntry{Policy: PolicyLocked, Value: raw(`"P"`)}, []LevelEntry{own(tenant, `"Acme"`)}, []string{`"P"`, `"P"`}, LevelPlatform, ModeParent},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := Resolve(tc.setting, tc.platform, tc.lower)
			if err != nil {
				t.Fatal(err)
			}
			if v := values(got); !slices.EqualFunc(v, tc.want, func(a, b string) bool { return Equal(raw(a), raw(b)) }) {
				t.Fatalf("values = %v, want %v", v, tc.want)
			}
			last := got[len(got)-1]
			if last.Source != tc.source || last.Mode != tc.mode {
				t.Errorf("last = source %s mode %s, want %s %s", last.Source, last.Mode, tc.source, tc.mode)
			}
		})
	}

	t.Run("own without a value is corrupt data", func(t *testing.T) {
		t.Parallel()
		_, err := Resolve(name, PlatformEntry{}, []LevelEntry{{Level: tenant, Exists: true, Mode: ModeOwn}})
		if !errs.Is(err, errs.CodeInternal) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("unknown mode", func(t *testing.T) {
		t.Parallel()
		_, err := Resolve(name, PlatformEntry{}, []LevelEntry{{Level: tenant, Exists: true, Mode: "bogus"}})
		if !errs.Is(err, errs.CodeInternal) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a value that does not fit its rule", func(t *testing.T) {
		t.Parallel()
		for _, s := range []Setting{strictLow, methods} {
			_, err := Resolve(s, PlatformEntry{}, []LevelEntry{own(tenant, `{"a":1}`)})
			if !errs.Is(err, errs.CodeInternal) {
				t.Errorf("%s: err = %v", s.Merge, err)
			}
		}
	})
	t.Run("version vector", func(t *testing.T) {
		t.Parallel()
		e := own(tenant, `"A"`)
		e.Version = 7
		got, err := Resolve(name, PlatformEntry{Version: 2}, []LevelEntry{e})
		if err != nil || got[0].Version != 2 || got[1].Version != 7 || got[1].Own == nil {
			t.Fatalf("got %+v, %v", got, err)
		}
	})
}

// TestCheckSave checks the override policies at save time.
//
// Covers: TEN-06
func TestCheckSave(t *testing.T) {
	t.Parallel()
	strictLow := def(MergeStrictest, LowerIsStricter, PolicyBounded, `1800`)
	strictHigh := def(MergeStrictest, HigherIsStricter, PolicyBounded, `365`)
	methods := def(MergeIntersection, "", PolicyBounded, `["a","b"]`)
	quota := def(MergeCeiling, "", PolicyBounded, `100`)
	tests := []struct {
		name    string
		setting Setting
		policy  Policy
		parent  string
		mode    Mode
		value   string
		code    errs.Code // "" for accepted
		pointer string
	}{
		{"locked allows parent", strictLow, PolicyLocked, `900`, ModeParent, ``, "", ""},
		{"locked refuses own", strictLow, PolicyLocked, `900`, ModeOwn, `600`, errs.CodePermissionDenied, ""},
		{"locked refuses default", strictLow, PolicyLocked, `900`, ModeDefault, ``, errs.CodePermissionDenied, ""},
		{"overridable is free", strictLow, PolicyOverridable, `900`, ModeOwn, `7200`, "", ""},
		{"bounded lower: tighter ok", strictLow, PolicyBounded, `900`, ModeOwn, `600`, "", ""},
		{"bounded lower: equal ok", strictLow, PolicyBounded, `900`, ModeOwn, `900`, "", ""},
		{"bounded lower: weaker refused", strictLow, PolicyBounded, `900`, ModeOwn, `901`, errs.CodeValidationFailed, "/value"},
		{"bounded higher: weaker refused", strictHigh, PolicyBounded, `365`, ModeOwn, `30`, errs.CodeValidationFailed, "/value"},
		{"bounded higher: stronger ok", strictHigh, PolicyBounded, `365`, ModeOwn, `400`, "", ""},
		{"bounded set: subset ok", methods, PolicyBounded, `["a","b"]`, ModeOwn, `["a"]`, "", ""},
		{"bounded set: outside refused", methods, PolicyBounded, `["a"]`, ModeOwn, `["a","z"]`, errs.CodeValidationFailed, "/value"},
		{"bounded ceiling: above refused", quota, PolicyBounded, `100`, ModeOwn, `101`, errs.CodeValidationFailed, "/value"},
		{"bounded default within limit", strictLow, PolicyBounded, `1800`, ModeDefault, ``, "", ""},
		{"bounded default outside limit", strictLow, PolicyBounded, `900`, ModeDefault, ``, errs.CodeValidationFailed, "/mode"},
		{"bounded set default outside", methods, PolicyBounded, `["a"]`, ModeDefault, ``, errs.CodeValidationFailed, "/mode"},
		{"bounded parent always ok", strictLow, PolicyBounded, `900`, ModeParent, ``, "", ""},
		{"additive is free", def(MergeUnion, "", PolicyAdditive, `[]`), PolicyAdditive, `["x"]`, ModeOwn, `["y"]`, "", ""},
		{"own needs a value", strictLow, PolicyOverridable, `900`, ModeOwn, ``, errs.CodeValidationFailed, "/value"},
		{"default takes no value", strictLow, PolicyOverridable, `900`, ModeDefault, `5`, errs.CodeValidationFailed, "/value"},
		{"unknown mode", strictLow, PolicyOverridable, `900`, "bogus", ``, errs.CodeValidationFailed, "/mode"},
		{"non-numeric value under a number rule", strictLow, PolicyBounded, `900`, ModeOwn, `"x"`, errs.CodeValidationFailed, "/value"},
		{"non-array value under a set rule", methods, PolicyBounded, `["a"]`, ModeOwn, `5`, errs.CodeValidationFailed, "/value"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := CheckSave(tc.setting, tc.policy, raw(tc.parent), LevelTenant, tc.mode, raw(tc.value))
			if tc.code == "" {
				if err != nil {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if !errs.Is(err, tc.code) {
				t.Fatalf("err = %v, want code %s", err, tc.code)
			}
			if tc.pointer != "" {
				fields := err.(*errs.Error).Fields() //nolint:errcheck,errorlint,forcetypeassert // the checked error is an *errs.Error
				if len(fields) != 1 || fields[0].Pointer != tc.pointer {
					t.Fatalf("fields = %+v, want pointer %s", fields, tc.pointer)
				}
			}
		})
	}
}
