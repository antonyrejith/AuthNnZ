// Package domain holds the rules of the config module that need no I/O:
// the setting definitions, the inheritance resolver with its merge rules and
// override policies (TEN-06), the entities of versioned configuration objects
// (DYN-01) and the JSON helpers they share. It imports only the standard
// library, platform/ids and platform/errs (ADR-0016).
package domain

// Level is a level of the configuration hierarchy (plan 15 §1). B0 has three;
// the resolver works over an ordered list, so organizations and applications
// plug in later (B3) between environment and the leaf.
type Level string

// Levels, from the root to the leaf.
const (
	LevelPlatform    Level = "platform"
	LevelTenant      Level = "tenant"
	LevelEnvironment Level = "environment"
)

// Levels returns the levels of the hierarchy from the root down.
func Levels() []Level { return []Level{LevelPlatform, LevelTenant, LevelEnvironment} }

// Valid reports whether l is a known level.
func (l Level) Valid() bool {
	switch l {
	case LevelPlatform, LevelTenant, LevelEnvironment:
		return true
	default:
		return false
	}
}

// Mode is how a level obtains the value of a setting (plan 15 §2.1). The
// Template mode arrives with templates (B3).
type Mode string

// Modes.
const (
	// ModeDefault uses the product default.
	ModeDefault Mode = "default"
	// ModeParent follows the parent's effective value, live.
	ModeParent Mode = "parent"
	// ModeOwn uses a value saved at the level.
	ModeOwn Mode = "own"
)

// Valid reports whether m is a known mode.
func (m Mode) Valid() bool { return m == ModeDefault || m == ModeParent || m == ModeOwn }

// ImplicitMode is the mode of a setting nobody has saved at level: a new
// tenant uses the product defaults, and an environment follows its tenant
// (plan 15 §2.2).
func ImplicitMode(l Level) Mode {
	if l == LevelEnvironment {
		return ModeParent
	}
	return ModeDefault
}

// Policy is the override policy of a setting (plan 15 §3.1). The platform
// sets it; it applies to every level below and cannot be loosened there.
type Policy string

// Policies.
const (
	PolicyOverridable Policy = "overridable"
	PolicyBounded     Policy = "bounded"
	PolicyAdditive    Policy = "additive"
	PolicyLocked      Policy = "locked"
)

// Valid reports whether p is a known policy.
func (p Policy) Valid() bool {
	switch p {
	case PolicyOverridable, PolicyBounded, PolicyAdditive, PolicyLocked:
		return true
	default:
		return false
	}
}

// MergeRule says how the values along the path combine (plan 15 §3.2).
type MergeRule string

// Merge rules.
const (
	// MergeMostSpecific takes the value of the lowest level.
	MergeMostSpecific MergeRule = "most_specific"
	// MergeStrictest takes the safest value on the path (numbers; see Order).
	MergeStrictest MergeRule = "strictest"
	// MergeIntersection keeps what every level allows (arrays of strings).
	MergeIntersection MergeRule = "intersection"
	// MergeUnion keeps everything any level adds (arrays of strings).
	MergeUnion MergeRule = "union"
	// MergeCeiling caps the child's value at the parent's (numbers).
	MergeCeiling MergeRule = "ceiling"
)

// Valid reports whether r is a known rule.
func (r MergeRule) Valid() bool {
	switch r {
	case MergeMostSpecific, MergeStrictest, MergeIntersection, MergeUnion, MergeCeiling:
		return true
	default:
		return false
	}
}

// Order says which end of a number line is the stricter one, for MergeStrictest.
type Order string

// Orders.
const (
	// LowerIsStricter: a shorter lifetime, a smaller limit is safer.
	LowerIsStricter Order = "lower_is_stricter"
	// HigherIsStricter: a longer retention, a higher minimum is safer.
	HigherIsStricter Order = "higher_is_stricter"
)
