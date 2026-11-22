package app

import (
	"encoding/json"
	"slices"
	"testing"

	"pgregory.net/rapid"

	"github.com/antonyrejith/portun/internal/modules/config/internal/domain"
)

func raw(s string) json.RawMessage { return json.RawMessage(s) }

func own(l domain.Level, v string) domain.LevelEntry {
	return domain.LevelEntry{Level: l, Exists: true, Mode: domain.ModeOwn, Value: raw(v)}
}

// ---- property tests (plan 15 §8)

// settingCase is a generated setting with a generated path of entries.
type settingCase struct {
	setting  domain.Setting
	platform domain.PlatformEntry
	lower    []domain.LevelEntry
}

func genValue(rt *rapid.T, merge domain.MergeRule, label string) json.RawMessage {
	switch merge {
	case domain.MergeStrictest, domain.MergeCeiling:
		return mustJSON(rapid.IntRange(0, 100).Draw(rt, label))
	case domain.MergeIntersection, domain.MergeUnion:
		return mustJSON(rapid.SliceOfNDistinct(rapid.SampledFrom([]string{"a", "b", "c", "d", "e"}), 0, 5, rapid.ID[string]).Draw(rt, label))
	case domain.MergeMostSpecific:
	}
	return mustJSON(rapid.SampledFrom([]string{"x", "y", "z"}).Draw(rt, label))
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func genCase(rt *rapid.T) settingCase {
	merge := rapid.SampledFrom([]domain.MergeRule{
		domain.MergeMostSpecific, domain.MergeStrictest, domain.MergeIntersection, domain.MergeUnion, domain.MergeCeiling,
	}).Draw(rt, "merge")
	var order domain.Order
	if merge == domain.MergeStrictest {
		order = rapid.SampledFrom([]domain.Order{domain.LowerIsStricter, domain.HigherIsStricter}).Draw(rt, "order")
	}
	policy := rapid.SampledFrom([]domain.Policy{domain.PolicyOverridable, domain.PolicyLocked}).Draw(rt, "policy")
	c := settingCase{setting: domain.Setting{Key: "p.q", Schema: raw(`{}`), Default: genValue(rt, merge, "default"), Merge: merge, Order: order, Policy: policy}}
	if rapid.Bool().Draw(rt, "platform value") {
		c.platform.Value = genValue(rt, merge, "platform")
	}
	for _, l := range []domain.Level{domain.LevelTenant, domain.LevelEnvironment} {
		e := domain.LevelEntry{Level: l}
		if rapid.Bool().Draw(rt, "exists "+string(l)) {
			e.Exists = true
			e.Mode = rapid.SampledFrom([]domain.Mode{domain.ModeDefault, domain.ModeParent, domain.ModeOwn}).Draw(rt, "mode "+string(l))
			if e.Mode == domain.ModeOwn {
				e.Value = genValue(rt, merge, "own "+string(l))
			}
		}
		c.lower = append(c.lower, e)
	}
	return c
}

func resolveCase(rt *rapid.T, c settingCase) []domain.Resolved {
	got, err := domain.Resolve(c.setting, c.platform, c.lower)
	if err != nil {
		rt.Fatalf("resolve: %v", err)
	}
	return got
}

func num(raw json.RawMessage) float64 {
	var f float64
	if err := json.Unmarshal(raw, &f); err != nil {
		panic(err)
	}
	return f
}

func strs(raw json.RawMessage) []string {
	var s []string
	if err := json.Unmarshal(raw, &s); err != nil {
		panic(err)
	}
	return s
}

// TestPropertyLockedNeverDiffers: a locked setting equals its parent at every level.
//
// Covers: TEN-06
func TestPropertyLockedNeverDiffers(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(rt *rapid.T) {
		c := genCase(rt)
		c.setting.Policy = domain.PolicyLocked
		got := resolveCase(rt, c)
		for i := 1; i < len(got); i++ {
			if !domain.Equal(got[i].Value, got[i-1].Value) {
				rt.Fatalf("locked value at %s = %s, parent %s", got[i].Level, got[i].Value, got[i-1].Value)
			}
		}
	})
}

// TestPropertyStrictestNeverWeaker: a strictest setting is never weaker than any ancestor's.
//
// Covers: TEN-06
func TestPropertyStrictestNeverWeaker(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(rt *rapid.T) {
		c := genCase(rt)
		c.setting.Merge = domain.MergeStrictest
		c.setting.Order = rapid.SampledFrom([]domain.Order{domain.LowerIsStricter, domain.HigherIsStricter}).Draw(rt, "order")
		c.setting.Default = genValue(rt, domain.MergeStrictest, "d")
		c.setting.Policy = domain.PolicyOverridable
		c.platform.Value = nil
		for i := range c.lower {
			if c.lower[i].Mode == domain.ModeOwn {
				c.lower[i].Value = genValue(rt, domain.MergeStrictest, "v")
			}
		}
		got := resolveCase(rt, c)
		for i := 1; i < len(got); i++ {
			cur, parent := num(got[i].Value), num(got[i-1].Value)
			if c.setting.Order == domain.LowerIsStricter && cur > parent || c.setting.Order == domain.HigherIsStricter && cur < parent {
				rt.Fatalf("%s value %v is weaker than its parent's %v (%s)", got[i].Level, cur, parent, c.setting.Order)
			}
		}
	})
}

// TestPropertyIntersectionNeverAllowsMore: an intersection never allows what an ancestor forbids.
//
// Covers: TEN-06
func TestPropertyIntersectionNeverAllowsMore(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(rt *rapid.T) {
		c := genCase(rt)
		c.setting.Merge, c.setting.Order, c.setting.Policy = domain.MergeIntersection, "", domain.PolicyOverridable
		c.setting.Default = genValue(rt, domain.MergeIntersection, "d")
		c.platform.Value = genValue(rt, domain.MergeIntersection, "p")
		for i := range c.lower {
			if c.lower[i].Mode == domain.ModeOwn {
				c.lower[i].Value = genValue(rt, domain.MergeIntersection, "v")
			}
		}
		got := resolveCase(rt, c)
		for i := 1; i < len(got); i++ {
			for _, v := range strs(got[i].Value) {
				if !slices.Contains(strs(got[i-1].Value), v) {
					rt.Fatalf("%s allows %q, which its parent forbids", got[i].Level, v)
				}
			}
		}
	})
}

// TestPropertyCeilingNeverExceeded: a child never exceeds a ceiling.
//
// Covers: TEN-06
func TestPropertyCeilingNeverExceeded(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(rt *rapid.T) {
		c := genCase(rt)
		c.setting.Merge, c.setting.Order, c.setting.Policy = domain.MergeCeiling, "", domain.PolicyOverridable
		c.setting.Default = genValue(rt, domain.MergeCeiling, "d")
		c.platform.Value = genValue(rt, domain.MergeCeiling, "p")
		for i := range c.lower {
			if c.lower[i].Mode == domain.ModeOwn {
				c.lower[i].Value = genValue(rt, domain.MergeCeiling, "v")
			}
		}
		got := resolveCase(rt, c)
		for i := 1; i < len(got); i++ {
			if num(got[i].Value) > num(got[i-1].Value) {
				rt.Fatalf("%s value %v exceeds the ceiling %v", got[i].Level, got[i].Value, got[i-1].Value)
			}
		}
	})
}

// TestPropertyParentRestoresInherited: setting a level's mode back to parent
// restores the inherited value, whatever it saved before.
//
// Covers: TEN-06
func TestPropertyParentRestoresInherited(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(rt *rapid.T) {
		c := genCase(rt)
		before := resolveCase(rt, c)
		idx := rapid.IntRange(0, len(c.lower)-1).Draw(rt, "level")
		c.lower[idx] = domain.LevelEntry{Level: c.lower[idx].Level, Exists: true, Mode: domain.ModeParent}
		after := resolveCase(rt, c)
		if !domain.Equal(after[idx+1].Value, after[idx].Value) {
			rt.Fatalf("mode parent at %s gave %s, parent has %s", c.lower[idx].Level, after[idx+1].Value, after[idx].Value)
		}
		// The levels above are untouched.
		for i := 0; i <= idx; i++ {
			if !domain.Equal(before[i].Value, after[i].Value) {
				rt.Fatalf("level %d changed", i)
			}
		}
	})
}

// TestPropertyAcceptedValueTakesEffect: a value CheckSave accepts under a
// bounded policy is the effective value (it never loses to the parent).
//
// Covers: TEN-06
func TestPropertyAcceptedValueTakesEffect(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(rt *rapid.T) {
		merge := rapid.SampledFrom([]domain.MergeRule{domain.MergeStrictest, domain.MergeIntersection, domain.MergeCeiling}).Draw(rt, "merge")
		s := domain.Setting{Key: "a.b", Schema: raw(`{}`), Default: genValue(rt, merge, "d"), Merge: merge, Policy: domain.PolicyBounded}
		if merge == domain.MergeStrictest {
			s.Order = rapid.SampledFrom([]domain.Order{domain.LowerIsStricter, domain.HigherIsStricter}).Draw(rt, "order")
		}
		parent, value := genValue(rt, merge, "parent"), genValue(rt, merge, "value")
		if domain.CheckSave(s, domain.PolicyBounded, parent, domain.LevelPlatform, domain.ModeOwn, value) != nil {
			return
		}
		got, err := domain.Resolve(s, domain.PlatformEntry{Value: parent}, []domain.LevelEntry{own(domain.LevelTenant, string(value))})
		if err != nil {
			rt.Fatal(err)
		}
		same := domain.Equal(got[1].Value, value)
		if merge == domain.MergeIntersection { // the order of a set is not significant
			a, b := strs(got[1].Value), strs(value)
			slices.Sort(a)
			slices.Sort(b)
			same = slices.Equal(a, b)
		}
		if !same {
			rt.Fatalf("accepted %s under parent %s but the effective value is %s", value, parent, got[1].Value)
		}
	})
}
