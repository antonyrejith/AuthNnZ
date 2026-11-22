package domain

import (
	"encoding/json"
	"regexp"

	"github.com/antonyrejith/portun/internal/platform/errs"
)

// Setting is the definition of a setting: its key, the JSON Schema of its
// value, the product default, how values combine along the hierarchy and the
// policy the platform applies by default. The merge rule is part of the
// setting's type; the policy can be changed at the platform level.
type Setting struct {
	// Key is dotted lowercase, for example "session.idle_timeout_seconds".
	Key string
	// Schema is a JSON Schema 2020-12 document for the value.
	Schema []byte
	// Default is the product default; it must satisfy Schema.
	Default json.RawMessage
	// Merge combines the values along the path.
	Merge MergeRule
	// Order is required for MergeStrictest and ignored otherwise.
	Order Order
	// Policy is the default override policy.
	Policy Policy
}

var keyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)*$`)

// ValidKey reports whether key is a well-formed setting key.
func ValidKey(key string) bool { return len(key) <= 128 && keyPattern.MatchString(key) }

// CheckPolicy checks that policy makes sense for the setting's merge rule:
// bounded needs a rule that defines limits (strictest, intersection,
// ceiling) and additive needs union.
func (s Setting) CheckPolicy(p Policy) error {
	switch {
	case !p.Valid():
		return errs.New(errs.CodeInternal, "config: unknown policy "+string(p)+" for setting "+s.Key)
	case p == PolicyBounded && s.Merge != MergeStrictest && s.Merge != MergeIntersection && s.Merge != MergeCeiling:
		return errs.New(errs.CodeInternal, "config: setting "+s.Key+": policy bounded needs merge rule strictest, intersection or ceiling")
	case p == PolicyAdditive && s.Merge != MergeUnion:
		return errs.New(errs.CodeInternal, "config: setting "+s.Key+": policy additive needs merge rule union")
	default:
		return nil
	}
}

// Check validates the definition apart from the schema (the application layer
// compiles it): key syntax, merge rule, order, policy and the shape of the
// default for the merge rule. Errors are errs.CodeInternal, because a
// definition comes from code.
func (s Setting) Check() error {
	fail := func(msg string) error { return errs.New(errs.CodeInternal, "config: setting "+s.Key+": "+msg) }
	switch {
	case !ValidKey(s.Key):
		return errs.New(errs.CodeInternal, "config: setting key "+quote(s.Key)+" is not dotted lowercase")
	case len(s.Schema) == 0:
		return fail("a schema is required")
	case len(s.Default) == 0 || !json.Valid(s.Default):
		return fail("the default is missing or not JSON")
	case !s.Merge.Valid():
		return fail("unknown merge rule")
	case s.Merge == MergeStrictest && s.Order != LowerIsStricter && s.Order != HigherIsStricter:
		return fail("merge rule strictest needs an order")
	case s.Merge != MergeStrictest && s.Order != "":
		return fail("an order only applies to merge rule strictest")
	}
	if s.Merge == MergeStrictest || s.Merge == MergeCeiling {
		if _, ok := asNumber(s.Default); !ok {
			return fail("merge rule " + string(s.Merge) + " needs a number default")
		}
	}
	if s.Merge == MergeIntersection || s.Merge == MergeUnion {
		if _, ok := asStrings(s.Default); !ok {
			return fail("merge rule " + string(s.Merge) + " needs an array of strings default")
		}
	}
	return s.CheckPolicy(s.Policy)
}

func quote(s string) string { return `"` + s + `"` }
