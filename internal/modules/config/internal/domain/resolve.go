package domain

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/antonyrejith/portun/internal/platform/errs"
)

// PlatformEntry is the platform's value and policy of a setting. Nil Value
// means the product default; an empty Policy means the definition's.
type PlatformEntry struct {
	Value   json.RawMessage
	Policy  Policy
	Version int64
}

// LevelEntry is what one level below the platform saved for a setting. When
// Exists is false the level is in its implicit mode (ImplicitMode).
type LevelEntry struct {
	Level   Level
	Exists  bool
	Mode    Mode
	Value   json.RawMessage // set in ModeOwn only
	Version int64
}

// Resolved is the outcome at one level.
type Resolved struct {
	Level Level
	// Mode is the mode in force: ModeParent when a locked policy overrides
	// what the level saved.
	Mode Mode
	// Own is the value saved at the level (ModeOwn), nil otherwise.
	Own json.RawMessage
	// Value is the effective value at the level after merging along the path.
	Value json.RawMessage
	// Source is the level the effective value comes from.
	Source Level
	Policy Policy
	// Version is the version of the level's row, 0 when nothing is saved.
	Version int64
}

// Resolve computes the effective value of s at every level of the path: the
// platform first, then each entry of lower in order (TEN-06). For each level
// the mode picks the value to contribute (the product default, the parent's
// effective value, or the level's own value), then the merge rule combines it
// with the parent's effective value. The locked policy overrides any saved
// mode: a locked setting equals its parent's at every level.
func Resolve(s Setting, p PlatformEntry, lower []LevelEntry) ([]Resolved, error) {
	policy := p.Policy
	if policy == "" {
		policy = s.Policy
	}
	root := Resolved{Level: LevelPlatform, Mode: ModeDefault, Value: s.Default, Source: LevelPlatform, Policy: policy, Version: p.Version}
	if p.Value != nil {
		root.Mode, root.Own, root.Value = ModeOwn, p.Value, p.Value
	}
	out := make([]Resolved, 0, len(lower)+1)
	out = append(out, root)
	for _, e := range lower {
		parent := out[len(out)-1]
		r, err := resolveLevel(s, policy, parent, e)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

func resolveLevel(s Setting, policy Policy, parent Resolved, e LevelEntry) (Resolved, error) {
	mode := e.Mode
	if !e.Exists {
		mode = ImplicitMode(e.Level)
	}
	r := Resolved{Level: e.Level, Mode: mode, Policy: policy, Version: e.Version}
	if policy == PolicyLocked {
		r.Mode, r.Value, r.Source = ModeParent, parent.Value, parent.Source
		return r, nil
	}
	var chosen json.RawMessage
	switch mode {
	case ModeDefault:
		chosen = s.Default
	case ModeParent:
		chosen = parent.Value
	case ModeOwn:
		if e.Value == nil {
			return Resolved{}, errs.New(errs.CodeInternal, fmt.Sprintf("config: setting %s at %s is in mode own without a value", s.Key, e.Level))
		}
		chosen, r.Own = e.Value, e.Value
	default:
		return Resolved{}, errs.New(errs.CodeInternal, fmt.Sprintf("config: setting %s at %s has unknown mode %q", s.Key, e.Level, mode))
	}
	merged, err := Merge(s, parent.Value, chosen)
	if err != nil {
		return Resolved{}, err
	}
	r.Value = merged
	r.Source = e.Level
	if Equal(merged, parent.Value) {
		r.Source = parent.Source
	}
	return r, nil
}

// Merge combines the parent's effective value with the value a level
// contributes, by the setting's merge rule. It is idempotent on the parent's
// value: Merge(parent, parent) equals parent.
func Merge(s Setting, parent, chosen json.RawMessage) (json.RawMessage, error) {
	bad := func() (json.RawMessage, error) {
		return nil, errs.New(errs.CodeInternal, "config: setting "+s.Key+" holds a value that does not fit its merge rule "+string(s.Merge))
	}
	switch s.Merge {
	case MergeMostSpecific:
		return chosen, nil
	case MergeStrictest, MergeCeiling:
		pf, ok1 := asNumber(parent)
		cf, ok2 := asNumber(chosen)
		if !ok1 || !ok2 {
			return bad()
		}
		pickChosen := cf < pf // lower wins: the ceiling and the LowerIsStricter order
		if s.Merge == MergeStrictest && s.Order == HigherIsStricter {
			pickChosen = cf > pf
		}
		if pickChosen {
			return chosen, nil
		}
		return parent, nil
	case MergeIntersection, MergeUnion:
		ps, ok1 := asStrings(parent)
		cs, ok2 := asStrings(chosen)
		if !ok1 || !ok2 {
			return bad()
		}
		if s.Merge == MergeIntersection {
			return stringsJSON(intersect(ps, cs)), nil
		}
		return stringsJSON(union(ps, cs)), nil
	default:
		return bad()
	}
}

func intersect(parent, child []string) []string {
	out := []string{}
	for _, v := range parent {
		if slices.Contains(child, v) && !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}

func union(parent, child []string) []string {
	out := []string{}
	for _, v := range slices.Concat(parent, child) {
		if !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}

func stringsJSON(v []string) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic("config: marshal strings: " + err.Error()) // []string always marshals
	}
	return b
}

// CheckSave decides whether a level may save mode (and, for ModeOwn, value)
// for a setting, given the override policy and the parent's effective value
// and its source level (TEN-11). It returns errs.CodePermissionDenied for a
// locked setting and errs.CodeValidationFailed, with a field error at /mode
// or /value that names the limit, for a bounded one. The value's JSON Schema
// is checked by the caller.
func CheckSave(s Setting, policy Policy, parent json.RawMessage, parentSource Level, mode Mode, value json.RawMessage) error {
	if !mode.Valid() {
		return errs.New(errs.CodeValidationFailed, "unknown mode").WithFields(field("/mode", "invalid", "mode must be default, parent or own"))
	}
	if mode == ModeOwn && len(value) == 0 {
		return errs.New(errs.CodeValidationFailed, "a value is required in mode own").WithFields(field("/value", "required", "mode own needs a value"))
	}
	if mode != ModeOwn && len(value) != 0 {
		return errs.New(errs.CodeValidationFailed, "a value is only allowed in mode own").WithFields(field("/value", "unexpected", "only mode own takes a value"))
	}
	if policy == PolicyLocked && mode != ModeParent {
		return errs.New(errs.CodePermissionDenied, "setting "+s.Key+" is locked by "+string(LevelPlatform)+"; only mode parent is allowed")
	}
	if policy != PolicyBounded || mode == ModeParent {
		return nil
	}
	candidate, pointer := value, "/value"
	if mode == ModeDefault {
		candidate, pointer = s.Default, "/mode"
	}
	if msg := outsideLimit(s, parent, candidate); msg != "" {
		if mode == ModeDefault {
			msg = "the product default is outside the limit: " + msg
		}
		return errs.New(errs.CodeValidationFailed, "setting "+s.Key+" is bounded by "+string(parentSource)+": "+msg).
			WithFields(field(pointer, "out_of_bounds", msg+" (limit set at "+string(parentSource)+")"))
	}
	return nil
}

func field(pointer, code, detail string) errs.FieldError {
	return errs.FieldError{Pointer: pointer, Code: code, Detail: detail}
}

// outsideLimit returns a description of how v breaks the parent's limit, or
// "" when v is within it.
func outsideLimit(s Setting, parent, v json.RawMessage) string {
	switch s.Merge {
	case MergeStrictest, MergeCeiling:
		return outsideNumberLimit(s, parent, v)
	case MergeIntersection:
		return outsideSetLimit(parent, v)
	case MergeMostSpecific, MergeUnion:
	}
	return ""
}

func outsideNumberLimit(s Setting, parent, v json.RawMessage) string {
	pf, ok1 := asNumber(parent)
	vf, ok2 := asNumber(v)
	switch {
	case !ok1 || !ok2:
		return "value is not a number"
	case s.Merge == MergeStrictest && s.Order == HigherIsStricter:
		if vf < pf {
			return fmt.Sprintf("value must be at least %s", parent)
		}
	case vf > pf && s.Merge == MergeCeiling:
		return fmt.Sprintf("value must not exceed the ceiling %s", parent)
	case vf > pf:
		return fmt.Sprintf("value must be at most %s", parent)
	}
	return ""
}

func outsideSetLimit(parent, v json.RawMessage) string {
	ps, ok1 := asStrings(parent)
	vs, ok2 := asStrings(v)
	if !ok1 || !ok2 {
		return "value is not an array of strings"
	}
	var extra []string
	for _, x := range vs {
		if !slices.Contains(ps, x) {
			extra = append(extra, x)
		}
	}
	if len(extra) > 0 {
		return fmt.Sprintf("value contains %v, outside the allowed set %s", extra, parent)
	}
	return ""
}
