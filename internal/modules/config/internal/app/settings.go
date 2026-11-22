package app

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/antonyrejith/portun/internal/modules/audit"
	"github.com/antonyrejith/portun/internal/modules/config/internal/domain"
	"github.com/antonyrejith/portun/internal/platform/db"
	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/ids"
	"github.com/antonyrejith/portun/internal/platform/scope"
)

// EffectiveSetting is the resolved state of one setting at one level.
type EffectiveSetting struct {
	Key string
	// Mode is the mode in force at the level.
	Mode domain.Mode
	// OwnValue is the value saved at the level (mode own), nil otherwise.
	OwnValue json.RawMessage
	// Value is the effective value after merging along the hierarchy.
	Value json.RawMessage
	// Source is the level the effective value comes from.
	Source domain.Level
	// Policy is the override policy in force.
	Policy domain.Policy
	// Versions is the version vector: the version of the row each level
	// contributed (0 when that level saved nothing).
	Versions map[domain.Level]int64
}

type settingKey struct {
	key string
	env ids.EnvironmentID // the nil ID for the tenant level
}

// settingsState is everything the resolver reads, loaded in one transaction.
type settingsState struct {
	platform map[string]domain.PlatformRow
	rows     map[settingKey]domain.SettingRow
}

func (s *Service) loadState(ctx context.Context, q db.Querier, t scope.Tenant) (settingsState, error) {
	platform, err := s.repo.PlatformSettings(ctx, q)
	if err != nil {
		return settingsState{}, err //nolint:wrapcheck // already an *errs.Error
	}
	rows, err := s.repo.SettingRows(ctx, q, t.TenantID, t.EnvironmentID)
	if err != nil {
		return settingsState{}, err //nolint:wrapcheck // already an *errs.Error
	}
	st := settingsState{platform: platform, rows: make(map[settingKey]domain.SettingRow, len(rows))}
	for _, r := range rows {
		st.rows[settingKey{r.Key, r.LevelEnvironmentID}] = r
	}
	return st, nil
}

// levelIndex orders the levels of the path.
func levelIndex(l domain.Level) int {
	return slices.Index(domain.Levels(), l)
}

// resolve computes the path of one setting from the platform down to level.
func resolve(def *settingDef, st settingsState, env ids.EnvironmentID, level domain.Level) ([]domain.Resolved, error) {
	var p domain.PlatformEntry
	if row, ok := st.platform[def.Key]; ok {
		p = domain.PlatformEntry{Value: row.Value, Policy: row.Policy, Version: row.Version}
	}
	var lower []domain.LevelEntry
	for _, l := range domain.Levels() {
		if l == domain.LevelPlatform || levelIndex(l) > levelIndex(level) {
			continue
		}
		e := domain.LevelEntry{Level: l}
		var levelEnv ids.EnvironmentID
		if l == domain.LevelEnvironment {
			levelEnv = env
		}
		if row, ok := st.rows[settingKey{def.Key, levelEnv}]; ok {
			e.Exists, e.Mode, e.Value, e.Version = true, row.Mode, row.Value, row.Version
		}
		lower = append(lower, e)
	}
	return domain.Resolve(def.Setting, p, lower) //nolint:wrapcheck // already an *errs.Error
}

func effective(def *settingDef, path []domain.Resolved) EffectiveSetting {
	last := path[len(path)-1]
	versions := make(map[domain.Level]int64, len(path))
	for _, r := range path {
		versions[r.Level] = r.Version
	}
	return EffectiveSetting{
		Key: def.Key, Mode: last.Mode, OwnValue: last.Own, Value: last.Value, Source: last.Source,
		Policy: last.Policy, Versions: versions,
	}
}

// effectiveForEnvironment returns the effective value of every registered
// setting for the scope's environment, for the bundle.
func (s *Service) effectiveForEnvironment(ctx context.Context, q db.Querier, t scope.Tenant) (map[string]json.RawMessage, error) {
	st, err := s.loadState(ctx, q, t)
	if err != nil {
		return nil, err
	}
	out := map[string]json.RawMessage{}
	for _, key := range s.reg.SettingKeys() {
		def, _ := s.reg.setting(key)
		path, err := resolve(def, st, t.EnvironmentID, domain.LevelEnvironment)
		if err != nil {
			return nil, err
		}
		out[key] = path[len(path)-1].Value
	}
	return out, nil
}

// levelScope checks that level is one a tenant can save settings at and that
// the scope has what the level needs.
func levelScope(ctx context.Context, level domain.Level) (scope.Tenant, error) {
	if level == domain.LevelTenant {
		t, err := scope.TenantFrom(ctx)
		return t, err //nolint:wrapcheck // already an *errs.Error
	}
	if level == domain.LevelEnvironment {
		return envScope(ctx)
	}
	return scope.Tenant{}, errs.New(errs.CodeInvalidRequest, "settings are saved at the tenant or environment level; the platform level has SetPlatformSetting")
}

func levelEnvOf(level domain.Level, t scope.Tenant) ids.EnvironmentID {
	if level == domain.LevelEnvironment {
		return t.EnvironmentID
	}
	return ids.EnvironmentID{}
}

// parentLevel is the level above l.
func parentLevel(l domain.Level) domain.Level {
	return domain.Levels()[levelIndex(l)-1]
}

// EffectiveSettings returns the effective settings for the scope's
// environment; keys nil means all registered settings.
func (s *Service) EffectiveSettings(ctx context.Context, keys []string) ([]EffectiveSetting, error) {
	return s.EffectiveSettingsAt(ctx, domain.LevelEnvironment, keys)
}

// EffectiveSettingsAt returns the effective settings at a level (tenant or
// environment): mode, own value, effective value, source, policy and the
// version vector. Keys nil means all registered settings.
func (s *Service) EffectiveSettingsAt(ctx context.Context, level domain.Level, keys []string) ([]EffectiveSetting, error) {
	t, err := levelScope(ctx, level)
	if err != nil {
		return nil, err
	}
	defs, err := s.selectDefs(keys)
	if err != nil {
		return nil, err
	}
	var out []EffectiveSetting
	err = s.read(ctx, func(ctx context.Context, q db.Querier) error {
		st, err := s.loadState(ctx, q, t)
		if err != nil {
			return err
		}
		for _, d := range defs {
			path, err := resolve(d, st, t.EnvironmentID, level)
			if err != nil {
				return err
			}
			out = append(out, effective(d, path))
		}
		return nil
	})
	return out, err
}

func (s *Service) selectDefs(keys []string) ([]*settingDef, error) {
	if keys == nil {
		keys = s.reg.SettingKeys()
	}
	defs := make([]*settingDef, 0, len(keys))
	for i, k := range keys {
		d, ok := s.reg.setting(k)
		if !ok {
			return nil, fieldError("/keys/"+strconv.Itoa(i), "unknown", "the setting "+strconv.Quote(k)+" is not registered")
		}
		defs = append(defs, d)
	}
	return defs, nil
}

// SetSetting saves the mode (and, for mode own, the value) of a setting at
// the tenant or environment level. expectedVersion must equal the saved
// row's version (0 when nothing is saved yet); a negative value skips the
// check. The override policy and merge limits of the levels above are
// enforced (TEN-06, TEN-11).
func (s *Service) SetSetting(ctx context.Context, level domain.Level, key string, mode domain.Mode, value json.RawMessage, expectedVersion int64) (domain.SettingRow, error) {
	t, err := levelScope(ctx, level)
	if err != nil {
		return domain.SettingRow{}, err
	}
	def, ok := s.reg.setting(key)
	if !ok {
		return domain.SettingRow{}, fieldError("/key", "unknown", "the setting "+strconv.Quote(key)+" is not registered")
	}
	if mode == domain.ModeOwn && len(value) > 0 {
		if issues := validateAgainst(def.schema, value); len(issues) > 0 {
			return domain.SettingRow{}, validationFailed("the value does not satisfy the schema of "+key, issues, "/value")
		}
	}
	var out domain.SettingRow
	err = s.auditor.Change(ctx, func(ctx context.Context, tx *audit.Tx) error {
		var err error
		out, err = s.saveSetting(ctx, tx, t, level, def, mode, value, expectedVersion)
		return err
	})
	if err != nil {
		return domain.SettingRow{}, err //nolint:wrapcheck // already an *errs.Error
	}
	return out, nil
}

// saveSetting checks the override policy against the parent's effective
// value, writes the row and records the change.
func (s *Service) saveSetting(ctx context.Context, tx *audit.Tx, t scope.Tenant, level domain.Level, def *settingDef, mode domain.Mode, value json.RawMessage, expectedVersion int64) (domain.SettingRow, error) {
	levelEnv := levelEnvOf(level, t)
	st, err := s.loadState(ctx, tx, t)
	if err != nil {
		return domain.SettingRow{}, err
	}
	path, err := resolve(def, st, t.EnvironmentID, parentLevel(level))
	if err != nil {
		return domain.SettingRow{}, err
	}
	parent := path[len(path)-1]
	if err := domain.CheckSave(def.Setting, parent.Policy, parent.Value, parent.Source, mode, value); err != nil {
		return domain.SettingRow{}, err //nolint:wrapcheck // already an *errs.Error
	}
	row, existed, err := s.repo.GetSettingRow(ctx, tx, t.TenantID, levelEnv, def.Key, true)
	if err != nil {
		return domain.SettingRow{}, err //nolint:wrapcheck // already an *errs.Error
	}
	if expectedVersion >= 0 && row.Version != expectedVersion {
		return domain.SettingRow{}, errs.New(errs.CodePreconditionFailed, "the setting changed since version "+strconv.FormatInt(expectedVersion, 10)+"; reload it")
	}
	next := domain.SettingRow{
		Key: def.Key, LevelEnvironmentID: levelEnv, Mode: mode, Value: value, Version: row.Version + 1,
		UpdatedBy: actorName(ctx), UpdatedAt: s.clock(),
	}
	if mode != domain.ModeOwn {
		next.Value = nil
	}
	if err := s.writeSettingRow(ctx, tx, t, next, existed); err != nil {
		return domain.SettingRow{}, err
	}
	return next, tx.Record(audit.Record{ //nolint:wrapcheck // already an *errs.Error
		Type: EventSettingChanged, Target: &audit.Target{Type: "config_setting", ID: def.Key, Name: string(level)},
		Before: settingState(row, existed, level), After: settingState(next, true, level),
		BeforeVersion: row.Version, AfterVersion: next.Version,
		Rows:    []audit.RowRef{settingRow(t.TenantID, levelEnv, def.Key)},
		Details: map[string]any{"mode": string(mode)},
	})
}

// writeSettingRow inserts or updates a row.
func (s *Service) writeSettingRow(ctx context.Context, tx *audit.Tx, t scope.Tenant, r domain.SettingRow, existed bool) error {
	if existed {
		return s.repo.UpdateSettingRow(ctx, tx, t.TenantID, r) //nolint:wrapcheck // already an *errs.Error
	}
	return s.repo.InsertSettingRow(ctx, tx, t.TenantID, r) //nolint:wrapcheck // already an *errs.Error
}

// settingState is the audited form of a setting row.
func settingState(r domain.SettingRow, existed bool, level domain.Level) map[string]any {
	if !existed {
		return map[string]any{"mode": string(domain.ImplicitMode(level))}
	}
	m := map[string]any{"mode": string(r.Mode)}
	if r.Value != nil {
		m["value"] = r.Value
	}
	return m
}

// matchKeys returns the registered keys selected by patterns: an exact key,
// "section.*" for a section, or "*" for everything.
func (s *Service) matchKeys(patterns []string) ([]*settingDef, error) {
	if len(patterns) == 0 {
		return nil, fieldError("/patterns", "required", "name at least one setting key or pattern such as \"mfa.*\"")
	}
	var defs []*settingDef
	seen := map[string]bool{}
	for i, p := range patterns {
		matched := false
		for _, k := range s.reg.SettingKeys() {
			if !matchPattern(p, k) {
				continue
			}
			matched = true
			if !seen[k] {
				seen[k] = true
				d, _ := s.reg.setting(k)
				defs = append(defs, d)
			}
		}
		if !matched && !strings.Contains(p, "*") {
			return nil, fieldError("/patterns/"+strconv.Itoa(i), "unknown", "the setting "+strconv.Quote(p)+" is not registered")
		}
	}
	return defs, nil
}

func matchPattern(pattern, key string) bool {
	switch {
	case pattern == "*":
		return true
	case strings.HasSuffix(pattern, ".*"):
		return strings.HasPrefix(key, strings.TrimSuffix(pattern, "*"))
	default:
		return pattern == key
	}
}

// resetItem is one setting a reset changes.
type resetItem struct {
	def     *settingDef
	before  domain.SettingRow
	existed bool
	after   domain.SettingRow
}

// planReset decides, for every matching setting, whether and how a reset
// changes it. The target is mode default; where the policy forbids it
// (locked, or a bounded setting whose product default is outside the
// parent's limits) it is mode parent.
func (s *Service) planReset(ctx context.Context, defs []*settingDef, st settingsState, t scope.Tenant, level domain.Level) ([]resetItem, error) {
	levelEnv := levelEnvOf(level, t)
	var items []resetItem
	for _, d := range defs {
		path, err := resolve(d, st, t.EnvironmentID, parentLevel(level))
		if err != nil {
			return nil, err
		}
		parent := path[len(path)-1]
		target := domain.ModeDefault
		if parent.Policy == domain.PolicyLocked ||
			(parent.Policy == domain.PolicyBounded && domain.CheckSave(d.Setting, parent.Policy, parent.Value, parent.Source, domain.ModeDefault, nil) != nil) {
			target = domain.ModeParent
		}
		row, existed := st.rows[settingKey{d.Key, levelEnv}]
		var changes bool
		switch {
		case existed:
			changes = row.Mode != target || row.Value != nil
		default:
			changes = domain.ImplicitMode(level) != target && parent.Policy != domain.PolicyLocked
		}
		if !changes {
			continue
		}
		items = append(items, resetItem{
			def: d, before: row, existed: existed,
			after: domain.SettingRow{
				Key: d.Key, LevelEnvironmentID: levelEnv, Mode: target, Version: row.Version + 1,
				UpdatedBy: actorName(ctx), UpdatedAt: s.clock(),
			},
		})
	}
	return items, nil
}

// previewReset resolves the planned items as they would be after the reset.
func previewReset(items []resetItem, st settingsState, env ids.EnvironmentID, level domain.Level) ([]EffectiveSetting, error) {
	sim := settingsState{platform: st.platform, rows: maps.Clone(st.rows)}
	for _, it := range items {
		sim.rows[settingKey{it.def.Key, it.after.LevelEnvironmentID}] = it.after
	}
	out := make([]EffectiveSetting, 0, len(items))
	for _, it := range items {
		path, err := resolve(it.def, sim, env, level)
		if err != nil {
			return nil, err
		}
		out = append(out, effective(it.def, path))
	}
	return out, nil
}

// ResetSettings returns the settings matching patterns ("mfa.*", an exact
// key, or "*") to mode default at the level, or to mode parent where the
// override policy forbids default. With dryRun it only returns the preview:
// the effective settings, as they would be after the reset, of the settings
// it would change. Without it, the change is applied in one audited
// transaction and the same list is returned. Nothing else is touched.
func (s *Service) ResetSettings(ctx context.Context, level domain.Level, patterns []string, dryRun bool) ([]EffectiveSetting, error) {
	t, err := levelScope(ctx, level)
	if err != nil {
		return nil, err
	}
	defs, err := s.matchKeys(patterns)
	if err != nil {
		return nil, err
	}
	var preview []EffectiveSetting
	err = s.read(ctx, func(ctx context.Context, q db.Querier) error {
		st, err := s.loadState(ctx, q, t)
		if err != nil {
			return err
		}
		items, err := s.planReset(ctx, defs, st, t, level)
		if err != nil {
			return err
		}
		preview, err = previewReset(items, st, t.EnvironmentID, level)
		return err
	})
	if err != nil || dryRun || len(preview) == 0 {
		return preview, err
	}
	var out []EffectiveSetting
	err = s.auditor.Change(ctx, func(ctx context.Context, tx *audit.Tx) error {
		var err error
		out, err = s.applyReset(ctx, tx, t, level, defs, patterns)
		return err
	})
	if err != nil {
		return nil, err //nolint:wrapcheck // already an *errs.Error
	}
	return out, nil
}

// applyReset plans the reset again inside the transaction, writes the rows
// and records one event.
func (s *Service) applyReset(ctx context.Context, tx *audit.Tx, t scope.Tenant, level domain.Level, defs []*settingDef, patterns []string) ([]EffectiveSetting, error) {
	st, err := s.loadState(ctx, tx, t)
	if err != nil {
		return nil, err
	}
	items, err := s.planReset(ctx, defs, st, t, level)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, errs.New(errs.CodeConflict, "the settings changed while they were being reset; try again")
	}
	out, err := previewReset(items, st, t.EnvironmentID, level)
	if err != nil {
		return nil, err
	}
	before, after := map[string]any{}, map[string]any{}
	var keys []string
	var rows []audit.RowRef
	for _, it := range items {
		if err := s.writeResetItem(ctx, tx, t, it); err != nil {
			return nil, err
		}
		keys = append(keys, it.def.Key)
		before[it.def.Key] = settingState(it.before, it.existed, level)
		after[it.def.Key] = settingState(it.after, true, level)
		rows = append(rows, settingRow(t.TenantID, it.after.LevelEnvironmentID, it.def.Key))
	}
	slices.Sort(keys)
	return out, tx.Record(audit.Record{ //nolint:wrapcheck // already an *errs.Error
		Type: EventSettingsReset, Target: &audit.Target{Type: "config_settings", ID: string(level), Name: strings.Join(patterns, ",")},
		Before: before, After: after, Rows: rows,
		Details: map[string]any{"keys": keys, "mode": string(domain.ModeDefault)},
	})
}

// writeResetItem locks the row, checks nobody changed it since the plan and
// writes it.
func (s *Service) writeResetItem(ctx context.Context, tx *audit.Tx, t scope.Tenant, it resetItem) error {
	cur, existed, err := s.repo.GetSettingRow(ctx, tx, t.TenantID, it.after.LevelEnvironmentID, it.def.Key, true)
	if err != nil {
		return err //nolint:wrapcheck // already an *errs.Error
	}
	if cur.Version != it.before.Version || existed != it.existed {
		return errs.New(errs.CodeConflict, "a setting changed while it was being reset; try again")
	}
	return s.writeSettingRow(ctx, tx, t, it.after, existed)
}

// SetPlatformSetting sets the platform's value and override policy of a
// setting: the baseline every tenant inherits.
func (s *Service) SetPlatformSetting(ctx context.Context, key string, value json.RawMessage, policy domain.Policy) error {
	def, ok := s.reg.setting(key)
	if !ok {
		return fieldError("/key", "unknown", "the setting "+strconv.Quote(key)+" is not registered")
	}
	if err := def.CheckPolicy(policy); err != nil {
		return fieldError("/policy", "invalid", err.Error())
	}
	if issues := validateAgainst(def.schema, value); len(issues) > 0 {
		return validationFailed("the value does not satisfy the schema of "+key, issues, "/value")
	}
	if _, err := domain.Merge(def.Setting, value, value); err != nil {
		return fieldError("/value", "invalid", "the value does not fit the merge rule of "+key)
	}
	return s.auditor.PlatformChange(ctx, func(ctx context.Context, tx *audit.Tx) error { //nolint:wrapcheck // already an *errs.Error
		next := domain.PlatformRow{Key: key, Value: value, Policy: policy, UpdatedAt: s.clock()}
		prev, existed, err := s.repo.UpsertPlatformSetting(ctx, tx, next)
		if err != nil {
			return err //nolint:wrapcheck // already an *errs.Error
		}
		var before any
		if existed {
			before = map[string]any{"value": prev.Value, "policy": string(prev.Policy)}
		}
		return tx.Record(audit.Record{
			Type: EventPlatformSettingChanged, Target: &audit.Target{Type: "config_setting", ID: key, Name: string(domain.LevelPlatform)},
			Before: before, After: map[string]any{"value": value, "policy": string(policy)},
			BeforeVersion: prev.Version, AfterVersion: prev.Version + 1,
			Rows: []audit.RowRef{rowRef("config.platform_settings", key)},
		})
	})
}
