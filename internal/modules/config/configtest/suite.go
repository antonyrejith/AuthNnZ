//go:build integration

package configtest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gowebpki/jcs"
	"github.com/jackc/pgx/v5"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/antonyrejith/portun/internal/modules/config"
	"github.com/antonyrejith/portun/internal/platform/bus"
	"github.com/antonyrejith/portun/internal/platform/db/dbtest"
	"github.com/antonyrejith/portun/internal/platform/db/page"
	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/ids"
	"github.com/antonyrejith/portun/internal/platform/outbox"
)

// RunSuite runs the config module's scenarios as parallel subtests, each on
// its own database. The module's packages each call it, so that the facade,
// the application layer and the PostgreSQL adapter are all measured by the
// same scenarios.
//
// Covers: DYN-01, DYN-05, TEN-06, GOV-11, GOV-12
func RunSuite(t *testing.T) {
	t.Helper()
	for _, s := range []struct {
		name string
		run  func(*testing.T)
	}{
		{"Objects", objectsScenario},
		{"Immutability", immutabilityScenario},
		{"CEL", celScenario},
		{"SettingsPolicies", settingsPolicies},
		{"SettingsValidation", settingsValidation},
		{"SettingsReset", settingsReset},
		{"PlatformSettings", platformSettings},
		{"Isolation", isolationScenario},
		{"ConcurrentPublish", concurrentPublish},
		{"Propagation", propagation},
		{"StaticStability", staticStability},
		{"Construction", construction},
	} {
		t.Run(s.name, func(t *testing.T) {
			t.Parallel()
			s.run(t)
		})
	}
}

// ---- helpers

func wantCode(t *testing.T, err error, code errs.Code) {
	t.Helper()
	if !errs.Is(err, code) {
		t.Fatalf("error = %v (code %q), want code %q", err, errs.CodeOf(err), code)
	}
}

func hasPointer(err error, pointer string) bool {
	var e *errs.Error
	if !errors.As(err, &e) {
		return false
	}
	for _, f := range e.Fields() {
		if f.Pointer == pointer {
			return true
		}
	}
	return false
}

func wantField(t *testing.T, err error, pointer string) {
	t.Helper()
	wantCode(t, err, errs.CodeValidationFailed)
	if !hasPointer(err, pointer) {
		t.Fatalf("error %v has no field error at %s", err, pointer)
	}
}

func ok(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func jsonEq(a, b json.RawMessage) bool {
	var av, bv any
	if json.Unmarshal(a, &av) != nil || json.Unmarshal(b, &bv) != nil {
		return false
	}
	return reflect.DeepEqual(av, bv)
}

func flow(cond string) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"title": "Login", "steps": []any{map[string]any{"id": "s1", "condition": cond}}})
	return b
}

// pollFor waits until cond holds, polling until the deadline.
func pollFor(t *testing.T, what string, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func auditTypes(t *testing.T, env *dbtest.Env) map[string]bool {
	t.Helper()
	conn, err := pgx.Connect(t.Context(), env.AdminURL.Reveal())
	ok(t, err)
	defer func() { _ = conn.Close(t.Context()) }()
	rows, err := conn.Query(t.Context(), `SELECT DISTINCT payload->>'type' FROM outbox.messages WHERE stream = 'AUDIT'`)
	ok(t, err)
	types, err := pgx.CollectRows(rows, pgx.RowTo[string])
	ok(t, err)
	out := map[string]bool{}
	for _, ty := range types {
		out[ty] = true
	}
	return out
}

// ---- objects: draft, validate, publish, rollback (DYN-01)

func objectsScenario(t *testing.T) { //nolint:gocognit // a scenario reads top to bottom
	t.Helper()
	f := NewFixture(t)
	m := f.Module
	tn, env := f.Tenant(t), Environment(t)
	ctx := Ctx(t, tn, env)

	_, _, err := m.CreateDraft(ctx, "nope", "login", flow("true"), "")
	wantField(t, err, "/kind")
	_, _, err = m.CreateDraft(ctx, KindFlow, "bad name!", flow("true"), "")
	wantField(t, err, "/name")
	_, _, err = m.CreateDraft(ctx, KindFlow, "login", json.RawMessage(`{"steps":[]}`), "")
	wantCode(t, err, errs.CodeValidationFailed)
	if !hasPointer(err, "/spec") && !hasPointer(err, "/spec/steps") {
		t.Fatalf("schema errors lack JSON Pointers under /spec: %v", err)
	}
	_, _, err = m.CreateDraft(ctx, KindFlow, "login", flow("true"), strings.Repeat("n", 1001))
	wantField(t, err, "/note")
	_, _, err = m.CreateDraft(Ctx(t, tn, ids.EnvironmentID{}), KindFlow, "login", flow("true"), "")
	wantCode(t, err, errs.CodeInvalidRequest)

	obj, v1, err := m.CreateDraft(ctx, KindFlow, "login", flow(`risk + 1`), "first")
	ok(t, err)
	if v1.Version != 1 || v1.Status != config.VersionDraft || obj.Version != 1 || obj.CurrentVersion != 0 {
		t.Fatalf("new draft: %+v %+v", obj, v1)
	}
	_, _, err = m.CreateDraft(ctx, KindFlow, "login", flow("true"), "")
	wantCode(t, err, errs.CodeConflict)

	// A draft is saved if it satisfies the schema; CEL is checked on Validate and Publish.
	rep, err := m.Validate(ctx, obj.ID, 1)
	ok(t, err)
	if rep.Valid || len(rep.Issues) != 1 || rep.Issues[0].Pointer != "/steps/0/condition" {
		t.Fatalf("report = %+v, want one CEL issue at /steps/0/condition", rep)
	}
	_, err = m.Publish(ctx, []ids.ConfigObjectID{obj.ID}, "nope")
	wantField(t, err, "/object_ids/0/spec/steps/0/condition")

	_, err = m.UpdateDraft(ctx, obj.ID, 99, flow("risk > 3"), "")
	wantCode(t, err, errs.CodePreconditionFailed)
	good := flow(`user.role == "admin" && risk > 3`)
	_, err = m.UpdateDraft(ctx, obj.ID, 1, good, "fix")
	ok(t, err)
	rep, err = m.Validate(ctx, obj.ID, 1)
	ok(t, err)
	if !rep.Valid {
		t.Fatalf("report = %+v, want valid", rep)
	}
	b0, err := m.Bundle(ctx)
	ok(t, err)
	if b0.Version != 0 || len(b0.Objects) != 0 {
		t.Fatalf("bundle before publishing = v%d with %d kinds, want the empty version 0", b0.Version, len(b0.Objects))
	}

	pub, err := m.Publish(ctx, []ids.ConfigObjectID{obj.ID, obj.ID}, "go live")
	ok(t, err)
	if pub.BundleVersion != 1 || !strings.HasPrefix(pub.Checksum, "sha256:") || len(pub.Objects) != 1 {
		t.Fatalf("publication = %+v", pub)
	}
	b1, err := m.Bundle(ctx)
	ok(t, err)
	if b1.Version != 1 || b1.Checksum != pub.Checksum || !jsonEq(b1.Objects[KindFlow]["login"], good) {
		t.Fatalf("bundle = v%d %s", b1.Version, b1.Checksum)
	}
	versions, _, err := m.ListVersions(ctx, obj.ID, page.Request{})
	ok(t, err)
	canon, err := jcs.Transform(good)
	ok(t, err)
	sum := sha256.Sum256(canon)
	if len(versions) != 1 || versions[0].Status != config.VersionPublished || versions[0].PublishedAt == nil ||
		versions[0].Checksum != "sha256:"+hex.EncodeToString(sum[:]) {
		t.Fatalf("versions = %+v", versions)
	}
	cur, err := m.GetObject(ctx, obj.ID)
	ok(t, err)
	if cur.CurrentVersion != 1 {
		t.Fatalf("current version = %d", cur.CurrentVersion)
	}

	// Publishing needs a draft.
	_, err = m.Publish(ctx, []ids.ConfigObjectID{obj.ID}, "")
	wantCode(t, err, errs.CodeConflict)

	// A second round: new draft, publish, then roll back to the first version.
	spec2 := flow("risk > 10")
	_, v2, err := m.CreateDraft(ctx, KindFlow, "login", spec2, "second")
	ok(t, err)
	if v2.Version != 2 {
		t.Fatalf("second draft is version %d", v2.Version)
	}
	_, err = m.Rollback(ctx, obj.ID, 1, "with an open draft")
	wantCode(t, err, errs.CodeConflict)
	pub2, err := m.Publish(ctx, []ids.ConfigObjectID{obj.ID}, "v2")
	ok(t, err)
	if pub2.BundleVersion != 2 {
		t.Fatalf("second bundle = %d", pub2.BundleVersion)
	}
	diff, err := m.Diff(ctx, obj.ID, 1, 2)
	ok(t, err)
	if len(diff) != 1 || diff[0].Op != "replace" || diff[0].Path != "/steps/0/condition" {
		t.Fatalf("diff = %+v", diff)
	}
	if _, err := m.Diff(ctx, obj.ID, 1, 9); !errs.Is(err, errs.CodeNotFound) {
		t.Fatalf("diff with a missing version: %v", err)
	}
	p1, next, err := m.ListVersions(ctx, obj.ID, page.Request{Size: 1})
	ok(t, err)
	if len(p1) != 1 || p1[0].Version != 2 || next == "" {
		t.Fatalf("first page = %+v %q", p1, next)
	}
	p2, next, err := m.ListVersions(ctx, obj.ID, page.Request{Size: 1, Token: next})
	ok(t, err)
	if len(p2) != 1 || p2[0].Version != 1 || next != "" {
		t.Fatalf("second page = %+v %q", p2, next)
	}

	pub3, err := m.Rollback(ctx, obj.ID, 1, "undo")
	ok(t, err)
	if pub3.BundleVersion != 3 {
		t.Fatalf("rollback bundle = %d", pub3.BundleVersion)
	}
	b3, err := m.Bundle(ctx)
	ok(t, err)
	if b3.Version != 3 || !jsonEq(b3.Objects[KindFlow]["login"], good) {
		t.Fatalf("bundle after rollback = v%d", b3.Version)
	}
	all, _, err := m.ListVersions(ctx, obj.ID, page.Request{})
	ok(t, err)
	if len(all) != 3 || all[0].Version != 3 || !jsonEq(all[0].Spec, good) || all[0].Checksum != versions[0].Checksum {
		t.Fatalf("versions after rollback = %+v", all)
	}
	_, err = m.Rollback(ctx, obj.ID, 3, "")
	wantCode(t, err, errs.CodeConflict)
	_, err = m.Rollback(ctx, obj.ID, 99, "")
	wantCode(t, err, errs.CodeNotFound)

	// A draft cannot be a rollback target.
	_, d4, err := m.CreateDraft(ctx, KindFlow, "login", flow("true"), "")
	ok(t, err)
	_, err = m.Rollback(ctx, obj.ID, d4.Version, "")
	wantCode(t, err, errs.CodeConflict)

	got := auditTypes(t, f.Env)
	for _, want := range []string{config.EventDraftSaved, config.EventVersionPublished, config.EventBundlePublished, config.EventRolledBack} {
		if !got[want] {
			t.Errorf("no %s audit record", want)
		}
	}
}

// ---- published versions are immutable (DYN-01)

func immutabilityScenario(t *testing.T) {
	t.Helper()
	f := NewFixture(t, "config.versions")
	m := f.Module
	tn, env := f.Tenant(t), Environment(t)
	ctx := Ctx(t, tn, env)
	obj, _, err := m.CreateDraft(ctx, KindFlow, "login", flow("true"), "")
	ok(t, err)
	_, err = m.Publish(ctx, []ids.ConfigObjectID{obj.ID}, "")
	ok(t, err)

	conn, err := pgx.Connect(t.Context(), f.Env.AdminURL.Reveal())
	ok(t, err)
	defer func() { _ = conn.Close(t.Context()) }()
	for _, stmt := range []string{
		`UPDATE config.versions SET spec = '{}'::jsonb`,
		`UPDATE config.versions SET checksum = 'sha256:x'`,
		`UPDATE config.versions SET change_note = 'x'`,
		`UPDATE config.versions SET status = 'draft'`,
		`DELETE FROM config.versions`,
	} {
		_, err := conn.Exec(t.Context(), stmt)
		if err == nil {
			t.Errorf("%q succeeded on a published version", stmt)
		}
	}
	// The application role has no DELETE or TRUNCATE on any config table.
	app, err := pgx.Connect(t.Context(), f.Env.AppURL.Reveal())
	ok(t, err)
	defer func() { _ = app.Close(t.Context()) }()
	for _, stmt := range []string{`DELETE FROM config.bundles`, `TRUNCATE config.objects`, `DELETE FROM config.settings`} {
		if _, err := app.Exec(t.Context(), stmt); err == nil {
			t.Errorf("the application role ran %q", stmt)
		}
	}
}

// ---- CEL evaluation from a bundle (DYN-05)

func celScenario(t *testing.T) {
	t.Helper()
	f := NewFixture(t)
	m := f.Module
	tn, env := f.Tenant(t), Environment(t)
	ctx := Ctx(t, tn, env)
	heavy := `items.map(x, items.map(y, items.map(z, x + y + z))).size() > 0`
	spec, _ := json.Marshal(map[string]any{
		"title": "Login", "greeting": `"Hello " + user.name`,
		"steps": []any{
			map[string]any{"id": "admin", "condition": `user.role == "admin" && risk > 5`},
			map[string]any{"id": "heavy", "condition": heavy},
		},
	})
	obj, _, err := m.CreateDraft(ctx, KindFlow, "login", spec, "")
	ok(t, err)
	_, err = m.Publish(ctx, []ids.ConfigObjectID{obj.ID}, "")
	ok(t, err)
	b, err := m.Bundle(ctx)
	ok(t, err)

	vars := func(role string, risk int64, n int) map[string]any {
		items := make([]any, n)
		for i := range items {
			items[i] = int64(i)
		}
		return map[string]any{"user": map[string]any{"role": role, "name": "Ann"}, "risk": risk, "items": items}
	}
	for _, c := range []struct {
		ptr  string
		vars map[string]any
		want any
	}{
		{"/steps/0/condition", vars("admin", 9, 0), true},
		{"/steps/0/condition", vars("admin", 1, 0), false},
		{"/steps/0/condition", vars("user", 9, 0), false},
		{"/greeting", vars("admin", 1, 0), "Hello Ann"},
		{"/steps/1/condition", vars("admin", 1, 3), true},
	} {
		got, err := b.Eval(KindFlow, "login", c.ptr, c.vars)
		if err != nil || got != c.want {
			t.Errorf("Eval %s = %v, %v; want %v", c.ptr, got, err, c.want)
		}
	}
	// The cost limit stops an expression that does too much work.
	if _, err := b.Eval(KindFlow, "login", "/steps/1/condition", vars("admin", 1, 100)); err == nil {
		t.Error("an expression over the cost limit was evaluated")
	}
	// A caller's canceled context stops evaluation too.
	cctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := b.EvalContext(cctx, KindFlow, "login", "/steps/1/condition", vars("admin", 1, 100)); err == nil {
		t.Error("a canceled evaluation succeeded")
	}
	if _, err := b.Eval(KindFlow, "login", "/steps/9/condition", vars("a", 1, 0)); !errs.Is(err, errs.CodeNotFound) {
		t.Errorf("unknown pointer: %v", err)
	}
	if _, err := b.Eval(KindFlow, "login", "/steps/0/condition", map[string]any{"risk": int64(9)}); err == nil {
		t.Error("evaluation with missing variables succeeded")
	}

	// A condition that is not boolean does not validate.
	obj2, _, err := m.CreateDraft(ctx, KindFlow, "bad-risk", flow(`risk + 1`), "")
	ok(t, err)
	rep, err := m.Validate(ctx, obj2.ID, 1)
	ok(t, err)
	if rep.Valid {
		t.Error("a non-boolean condition validated")
	}
}

// ---- settings: modes, policies, merge rules, versions (TEN-06)

func settingsPolicies(t *testing.T) { //nolint:gocognit // a scenario reads top to bottom
	t.Helper()
	f := NewFixture(t)
	m := f.Module
	tn, env := f.Tenant(t), Environment(t)
	tctx, ectx := Ctx(t, tn, ids.EnvironmentID{}), Ctx(t, tn, env)
	eff := func(ctx context.Context, key string) config.EffectiveSetting {
		t.Helper()
		out, err := m.EffectiveSettings(ctx, []string{key})
		ok(t, err)
		return out[0]
	}
	set := func(ctx context.Context, level config.Level, key string, mode config.Mode, value string, expected int64) (config.SettingValue, error) {
		var v json.RawMessage
		if value != "" {
			v = json.RawMessage(value)
		}
		return m.SetSetting(ctx, level, key, mode, v, expected)
	}

	// Nothing changed: every value is the product default (TEN-06).
	all, err := m.EffectiveSettings(ectx, nil)
	ok(t, err)
	if len(all) != len(Settings()) {
		t.Fatalf("effective settings = %d, want %d", len(all), len(Settings()))
	}
	byKey := map[string]config.EffectiveSetting{}
	for _, e := range all {
		byKey[e.Key] = e
	}
	for _, def := range Settings() {
		e := byKey[def.Key]
		if !jsonEq(e.Value, def.Default) || e.Mode != config.ModeParent || e.Source != config.LevelPlatform || e.Policy != def.Policy || e.OwnValue != nil {
			t.Errorf("%s = %+v, want the product default from the platform", def.Key, e)
		}
		for l, v := range e.Versions {
			if v != 0 {
				t.Errorf("%s version at %s = %d, want 0", def.Key, l, v)
			}
		}
	}

	// Bounded, strictest (lower is stricter): session timeout.
	_, err = set(tctx, config.LevelTenant, SessionIdleTimeout, config.ModeOwn, `3600`, 0)
	wantField(t, err, "/value")
	row, err := set(tctx, config.LevelTenant, SessionIdleTimeout, config.ModeOwn, `900`, 0)
	ok(t, err)
	if row.Version != 1 || row.Level() != config.LevelTenant {
		t.Fatalf("row = %+v", row)
	}
	_, err = set(tctx, config.LevelTenant, SessionIdleTimeout, config.ModeOwn, `800`, 0)
	wantCode(t, err, errs.CodePreconditionFailed)
	e := eff(ectx, SessionIdleTimeout)
	if string(e.Value) != "900" || e.Source != config.LevelTenant || e.Mode != config.ModeParent || e.Versions[config.LevelTenant] != 1 {
		t.Fatalf("environment follows the tenant: %+v", e)
	}
	_, err = set(ectx, config.LevelEnvironment, SessionIdleTimeout, config.ModeOwn, `1200`, 0)
	wantField(t, err, "/value")
	er, err := set(ectx, config.LevelEnvironment, SessionIdleTimeout, config.ModeOwn, `600`, 0)
	ok(t, err)
	if er.Level() != config.LevelEnvironment {
		t.Fatalf("level of %+v", er)
	}
	e = eff(ectx, SessionIdleTimeout)
	if string(e.Value) != "600" || e.Source != config.LevelEnvironment || string(e.OwnValue) != "600" || e.Mode != config.ModeOwn {
		t.Fatalf("environment own: %+v", e)
	}
	// Back to parent restores the inherited value.
	_, err = set(ectx, config.LevelEnvironment, SessionIdleTimeout, config.ModeParent, "", er.Version)
	ok(t, err)
	if e = eff(ectx, SessionIdleTimeout); string(e.Value) != "900" || e.OwnValue != nil {
		t.Fatalf("after parent: %+v", e)
	}
	// The product default (1800) is within the platform's limit (1800).
	_, err = set(tctx, config.LevelTenant, SessionIdleTimeout, config.ModeDefault, "", -1)
	ok(t, err)
	if e = eff(ectx, SessionIdleTimeout); string(e.Value) != "1800" {
		t.Fatalf("after default: %+v", e)
	}

	// Locked at the platform: only mode parent.
	_, err = set(tctx, config.LevelTenant, AuditRetentionDays, config.ModeOwn, `400`, 0)
	wantCode(t, err, errs.CodePermissionDenied)
	_, err = set(tctx, config.LevelTenant, AuditRetentionDays, config.ModeDefault, "", 0)
	wantCode(t, err, errs.CodePermissionDenied)
	_, err = set(tctx, config.LevelTenant, AuditRetentionDays, config.ModeParent, "", 0)
	ok(t, err)
	if e = eff(ectx, AuditRetentionDays); string(e.Value) != "365" || e.Policy != config.PolicyLocked {
		t.Fatalf("locked: %+v", e)
	}

	// Intersection, bounded: a subset of the parent's methods.
	_, err = set(tctx, config.LevelTenant, LoginMethods, config.ModeOwn, `["sms"]`, 0)
	wantField(t, err, "/value")
	_, err = set(tctx, config.LevelTenant, LoginMethods, config.ModeOwn, `["password","passkey"]`, 0)
	ok(t, err)
	_, err = set(ectx, config.LevelEnvironment, LoginMethods, config.ModeDefault, "", 0)
	wantField(t, err, "/mode") // the product default allows email_otp
	_, err = set(ectx, config.LevelEnvironment, LoginMethods, config.ModeOwn, `["passkey"]`, 0)
	ok(t, err)
	if e = eff(ectx, LoginMethods); string(e.Value) != `["passkey"]` {
		t.Fatalf("intersection: %+v", e)
	}

	// Union, additive: the parent's roles always remain.
	_, err = set(tctx, config.LevelTenant, CustomRoles, config.ModeOwn, `["auditor"]`, 0)
	ok(t, err)
	_, err = set(ectx, config.LevelEnvironment, CustomRoles, config.ModeOwn, `["support"]`, 0)
	ok(t, err)
	if e = eff(ectx, CustomRoles); string(e.Value) != `["auditor","support"]` {
		t.Fatalf("union: %+v", e)
	}

	// Ceiling, bounded.
	_, err = set(tctx, config.LevelTenant, MaxUsers, config.ModeOwn, `20000`, 0)
	wantField(t, err, "/value")
	_, err = set(tctx, config.LevelTenant, MaxUsers, config.ModeOwn, `5000`, 0)
	ok(t, err)
	_, err = set(ectx, config.LevelEnvironment, MaxUsers, config.ModeOwn, `6000`, 0)
	wantField(t, err, "/value")
	_, err = set(ectx, config.LevelEnvironment, MaxUsers, config.ModeOwn, `4000`, 0)
	ok(t, err)
	if e = eff(ectx, MaxUsers); string(e.Value) != "4000" {
		t.Fatalf("ceiling: %+v", e)
	}

	// Most specific, overridable.
	_, err = set(tctx, config.LevelTenant, ProductName, config.ModeOwn, `"Acme"`, 0)
	ok(t, err)
	if e = eff(ectx, ProductName); string(e.Value) != `"Acme"` || e.Source != config.LevelTenant {
		t.Fatalf("most specific: %+v", e)
	}
	_, err = set(ectx, config.LevelEnvironment, ProductName, config.ModeOwn, `"Acme Dev"`, 0)
	ok(t, err)
	if e = eff(ectx, ProductName); string(e.Value) != `"Acme Dev"` {
		t.Fatalf("most specific own: %+v", e)
	}

	// The effective settings reach the runtime bundle when the environment is published.
	pub, err := m.Publish(ectx, nil, "settings")
	ok(t, err)
	b, err := m.Bundle(ectx)
	ok(t, err)
	if b.Version != pub.BundleVersion || string(b.Settings[ProductName]) != `"Acme Dev"` || string(b.Settings[LoginMethods]) != `["passkey"]` {
		t.Fatalf("bundle settings = %v (v%d)", b.Settings, b.Version)
	}
	got := auditTypes(t, f.Env)
	if !got[config.EventSettingChanged] || !got[config.EventBundlePublished] {
		t.Errorf("audit types = %v", got)
	}
}

func settingsValidation(t *testing.T) {
	t.Helper()
	f := NewFixture(t)
	m := f.Module
	tn, env := f.Tenant(t), Environment(t)
	tctx, ectx := Ctx(t, tn, ids.EnvironmentID{}), Ctx(t, tn, env)

	_, err := m.SetSetting(tctx, config.LevelPlatform, ProductName, config.ModeOwn, json.RawMessage(`"x"`), 0)
	wantCode(t, err, errs.CodeInvalidRequest)
	_, err = m.SetSetting(tctx, config.LevelEnvironment, ProductName, config.ModeOwn, json.RawMessage(`"x"`), 0)
	wantCode(t, err, errs.CodeInvalidRequest)
	_, err = m.SetSetting(ectx, config.LevelTenant, "no.such", config.ModeOwn, json.RawMessage(`"x"`), 0)
	wantField(t, err, "/key")
	_, err = m.SetSetting(ectx, config.LevelTenant, ProductName, config.ModeOwn, json.RawMessage(`""`), 0)
	wantField(t, err, "/value")
	_, err = m.SetSetting(ectx, config.LevelTenant, ProductName, config.ModeOwn, nil, 0)
	wantField(t, err, "/value")
	_, err = m.SetSetting(ectx, config.LevelTenant, ProductName, config.ModeDefault, json.RawMessage(`"x"`), 0)
	wantField(t, err, "/value")
	_, err = m.SetSetting(ectx, config.LevelTenant, ProductName, "bogus", nil, 0)
	wantField(t, err, "/mode")
	_, err = m.EffectiveSettingsAt(tctx, config.LevelPlatform, nil)
	wantCode(t, err, errs.CodeInvalidRequest)
	_, err = m.EffectiveSettings(tctx, nil)
	wantCode(t, err, errs.CodeInvalidRequest)
	_, err = m.EffectiveSettings(ectx, []string{"no.such"})
	wantField(t, err, "/keys/0")
	// The tenant level needs no environment.
	if out, err := m.EffectiveSettingsAt(tctx, config.LevelTenant, nil); err != nil || len(out) != len(Settings()) {
		t.Fatalf("tenant level: %d, %v", len(out), err)
	}
	// Mode of an untouched tenant setting is default; an environment's is parent.
	out, err := m.EffectiveSettingsAt(tctx, config.LevelTenant, []string{ProductName})
	ok(t, err)
	if out[0].Mode != config.ModeDefault {
		t.Fatalf("tenant mode = %s", out[0].Mode)
	}
	_, err = m.Bundle(tctx)
	wantCode(t, err, errs.CodeInvalidRequest)
}

func settingsReset(t *testing.T) {
	t.Helper()
	f := NewFixture(t)
	m := f.Module
	tn, env := f.Tenant(t), Environment(t)
	tctx, ectx := Ctx(t, tn, ids.EnvironmentID{}), Ctx(t, tn, env)

	_, err := m.SetSetting(tctx, config.LevelTenant, ProductName, config.ModeOwn, json.RawMessage(`"Acme"`), 0)
	ok(t, err)
	_, err = m.SetSetting(tctx, config.LevelTenant, CustomRoles, config.ModeOwn, json.RawMessage(`["a"]`), 0)
	ok(t, err)

	// A dry run previews and writes nothing.
	prev, err := m.ResetSettings(tctx, config.LevelTenant, []string{"branding.*"}, true)
	ok(t, err)
	if len(prev) != 1 || prev[0].Key != ProductName || string(prev[0].Value) != `"Portun"` || prev[0].Mode != config.ModeDefault {
		t.Fatalf("preview = %+v", prev)
	}
	cur, err := m.EffectiveSettingsAt(tctx, config.LevelTenant, []string{ProductName})
	ok(t, err)
	if string(cur[0].Value) != `"Acme"` {
		t.Fatalf("a dry run changed the setting: %+v", cur[0])
	}
	applied, err := m.ResetSettings(tctx, config.LevelTenant, []string{"branding.*", ProductName}, false)
	ok(t, err)
	if len(applied) != 1 || string(applied[0].Value) != `"Portun"` {
		t.Fatalf("applied = %+v", applied)
	}
	cur, err = m.EffectiveSettingsAt(tctx, config.LevelTenant, []string{ProductName, CustomRoles})
	ok(t, err)
	if string(cur[0].Value) != `"Portun"` || cur[0].Mode != config.ModeDefault || string(cur[1].Value) != `["a"]` {
		t.Fatalf("after reset: %+v", cur)
	}
	// Nothing left to reset.
	again, err := m.ResetSettings(tctx, config.LevelTenant, []string{"branding.*"}, false)
	ok(t, err)
	if len(again) != 0 {
		t.Fatalf("second reset changed %+v", again)
	}

	// Everything at the environment level: default where allowed, parent where the policy forbids it.
	_, err = m.SetSetting(ectx, config.LevelEnvironment, ProductName, config.ModeOwn, json.RawMessage(`"Dev"`), 0)
	ok(t, err)
	_, err = m.SetSetting(ectx, config.LevelEnvironment, AuditRetentionDays, config.ModeParent, nil, 0)
	ok(t, err)
	all, err := m.ResetSettings(ectx, config.LevelEnvironment, []string{"*"}, false)
	ok(t, err)
	modes := map[string]config.Mode{}
	for _, e := range all {
		modes[e.Key] = e.Mode
	}
	if modes[ProductName] != config.ModeDefault {
		t.Fatalf("reset modes = %v", modes)
	}
	if _, has := modes[AuditRetentionDays]; has {
		t.Fatalf("a locked setting already in mode parent was reset: %v", modes)
	}

	_, err = m.ResetSettings(tctx, config.LevelTenant, []string{"no.such"}, false)
	wantField(t, err, "/patterns/0")
	_, err = m.ResetSettings(tctx, config.LevelTenant, nil, false)
	wantField(t, err, "/patterns")
	_, err = m.ResetSettings(tctx, config.LevelPlatform, []string{"*"}, false)
	wantCode(t, err, errs.CodeInvalidRequest)
	if !auditTypes(t, f.Env)[config.EventSettingsReset] {
		t.Error("no settings_reset audit record")
	}
}

func platformSettings(t *testing.T) {
	t.Helper()
	f := NewFixture(t)
	m := f.Module
	tn, env := f.Tenant(t), Environment(t)
	tctx, ectx := Ctx(t, tn, ids.EnvironmentID{}), Ctx(t, tn, env)
	pctx := PlatformCtx(t)

	wantField(t, m.SetPlatformSetting(pctx, "no.such", json.RawMessage(`1`), config.PolicyBounded), "/key")
	wantField(t, m.SetPlatformSetting(pctx, SessionIdleTimeout, json.RawMessage(`900`), config.PolicyAdditive), "/policy")
	wantField(t, m.SetPlatformSetting(pctx, SessionIdleTimeout, json.RawMessage(`"x"`), config.PolicyBounded), "/value")
	ok(t, m.SetPlatformSetting(pctx, SessionIdleTimeout, json.RawMessage(`900`), config.PolicyBounded))
	ok(t, m.SetPlatformSetting(pctx, SessionIdleTimeout, json.RawMessage(`600`), config.PolicyBounded))

	// A tenant in mode default still gets the stricter platform minimum.
	e, err := m.EffectiveSettings(ectx, []string{SessionIdleTimeout})
	ok(t, err)
	if string(e[0].Value) != "600" || e[0].Source != config.LevelPlatform || e[0].Versions[config.LevelPlatform] != 2 {
		t.Fatalf("platform baseline: %+v", e[0])
	}
	// Neither an own value above it nor the weaker product default is allowed.
	_, err = m.SetSetting(tctx, config.LevelTenant, SessionIdleTimeout, config.ModeOwn, json.RawMessage(`700`), 0)
	wantField(t, err, "/value")
	_, err = m.SetSetting(tctx, config.LevelTenant, SessionIdleTimeout, config.ModeDefault, nil, 0)
	wantField(t, err, "/mode")
	_, err = m.SetSetting(tctx, config.LevelTenant, SessionIdleTimeout, config.ModeOwn, json.RawMessage(`300`), 0)
	ok(t, err)
	// Reset goes to parent where default is outside the limit.
	out, err := m.ResetSettings(tctx, config.LevelTenant, []string{SessionIdleTimeout}, false)
	ok(t, err)
	if len(out) != 1 || out[0].Mode != config.ModeParent || string(out[0].Value) != "600" {
		t.Fatalf("reset under a platform minimum: %+v", out)
	}
	// Locking at the platform overrides what a tenant saved before.
	ok(t, m.SetPlatformSetting(pctx, ProductName, json.RawMessage(`"Portun Cloud"`), config.PolicyLocked))
	_, err = m.SetSetting(tctx, config.LevelTenant, ProductName, config.ModeOwn, json.RawMessage(`"Acme"`), 0)
	wantCode(t, err, errs.CodePermissionDenied)
	pe, err := m.EffectiveSettings(ectx, []string{ProductName})
	ok(t, err)
	if string(pe[0].Value) != `"Portun Cloud"` || pe[0].Policy != config.PolicyLocked {
		t.Fatalf("locked platform value: %+v", pe[0])
	}
	if !auditTypes(t, f.Env)[config.EventPlatformSettingChanged] {
		t.Error("no platform_setting_changed audit record")
	}
}

// ---- isolation between tenants and environments

func isolationScenario(t *testing.T) {
	t.Helper()
	f := NewFixture(t)
	m := f.Module
	a, b := f.Tenant(t), f.Tenant(t)
	env, env2 := Environment(t), Environment(t)
	actx, bctx, actx2 := Ctx(t, a, env), Ctx(t, b, env), Ctx(t, a, env2)

	obj, _, err := m.CreateDraft(actx, KindFlow, "login", flow("true"), "")
	ok(t, err)
	_, err = m.Publish(actx, []ids.ConfigObjectID{obj.ID}, "")
	ok(t, err)
	_, err = m.SetSetting(Ctx(t, a, ids.EnvironmentID{}), config.LevelTenant, ProductName, config.ModeOwn, json.RawMessage(`"Acme"`), 0)
	ok(t, err)

	for name, c := range map[string]context.Context{"another tenant": bctx, "another environment": actx2} {
		if _, err := m.GetObject(c, obj.ID); !errs.Is(err, errs.CodeNotFound) {
			t.Errorf("%s: GetObject = %v, want not found", name, err)
		}
		if _, _, err := m.ListVersions(c, obj.ID, page.Request{}); !errs.Is(err, errs.CodeNotFound) {
			t.Errorf("%s: ListVersions = %v, want not found", name, err)
		}
		if _, err := m.Validate(c, obj.ID, 1); !errs.Is(err, errs.CodeNotFound) {
			t.Errorf("%s: Validate = %v, want not found", name, err)
		}
		if _, err := m.UpdateDraft(c, obj.ID, 1, flow("true"), ""); !errs.Is(err, errs.CodeNotFound) {
			t.Errorf("%s: UpdateDraft = %v, want not found", name, err)
		}
		bun, err := m.Bundle(c)
		ok(t, err)
		if bun.Version != 0 || len(bun.Objects) != 0 {
			t.Errorf("%s: bundle v%d holds %d kinds", name, bun.Version, len(bun.Objects))
		}
	}
	e, err := m.EffectiveSettings(bctx, []string{ProductName})
	ok(t, err)
	if string(e[0].Value) != `"Portun"` {
		t.Errorf("tenant B sees tenant A's setting: %s", e[0].Value)
	}
	// Two tenants can use the same object name.
	if _, _, err := m.CreateDraft(bctx, KindFlow, "login", flow("true"), ""); err != nil {
		t.Errorf("tenant B cannot reuse a name: %v", err)
	}
	dbtest.AssertRLS(t, f.Env, "config")
}

func concurrentPublish(t *testing.T) {
	t.Helper()
	f := NewFixture(t)
	m := f.Module
	tn, env := f.Tenant(t), Environment(t)
	ctx := Ctx(t, tn, env)
	const n = 4
	versions := make([]int64, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, err := m.Publish(ctx, nil, "concurrent")
			if err != nil {
				t.Errorf("publish: %v", err)
				return
			}
			versions[i] = p.BundleVersion
		}()
	}
	wg.Wait()
	seen := map[int64]bool{}
	for _, v := range versions {
		if v < 1 || v > n || seen[v] {
			t.Fatalf("bundle versions = %v, want 1..%d once each", versions, n)
		}
		seen[v] = true
	}
}

// ---- propagation over the bus and by polling (DYN-01)

type cluster struct {
	srv *bus.EmbeddedServer
	bus *bus.Bus
}

func startCluster(t *testing.T, f *Fixture) *cluster {
	t.Helper()
	srv, err := bus.StartEmbedded(t.Context(), bus.EmbeddedConfig{StoreDir: t.TempDir()}, nil)
	ok(t, err)
	t.Cleanup(srv.Shutdown)
	b, err := bus.Connect(t.Context(), bus.Config{
		AllowInsecure: true, URL: srv.ClientURL(), Credentials: srv.Token(),
		ReconnectWait: 50 * time.Millisecond, DrainTimeout: 500 * time.Millisecond,
	}, nil)
	ok(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = b.Close(ctx)
	})
	ok(t, b.EnsureStreams(t.Context()))
	rctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = outbox.NewRelay(f.Env.DB, b, outbox.RelayConfig{PollInterval: 20 * time.Millisecond, ErrorBackoff: 50 * time.Millisecond}, nil).Run(rctx)
	}()
	t.Cleanup(func() { cancel(); <-done })
	return &cluster{srv: srv, bus: b}
}

// run starts m's Run loop until the test ends.
func run(t *testing.T, m *config.Module) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := m.Run(ctx); err != nil {
			t.Errorf("Run: %v", err)
		}
	}()
	t.Cleanup(func() { cancel(); <-done })
}

func bundleVersion(t *testing.T, ctx context.Context, m *config.Module) int64 { //nolint:revive // test helper: t first
	t.Helper()
	b, err := m.Bundle(ctx)
	if err != nil {
		t.Errorf("Bundle: %v", err)
		return -1
	}
	return b.Version
}

// waitConsumer waits until the durable consumer of an instance exists on the
// server, so that a message published afterwards is delivered to it.
func waitConsumer(t *testing.T, srv *bus.EmbeddedServer, instance string) {
	t.Helper()
	nc, err := nats.Connect(srv.ClientURL(), nats.Token(srv.Token().Reveal()))
	ok(t, err)
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	ok(t, err)
	pollFor(t, "the consumer of "+instance, 10*time.Second, func() bool {
		_, err := js.Consumer(t.Context(), bus.StreamConfig, "config-"+instance)
		return err == nil
	})
}

func propagation(t *testing.T) {
	t.Helper()
	f := NewFixture(t)
	c := startCluster(t, f)
	tn, env := f.Tenant(t), Environment(t)
	ctx := Ctx(t, tn, env)

	publisher := f.Module
	// "via-bus" only learns of bundles from notifications (its poll interval is an hour);
	// "poller" has a short interval, so it also keeps up when the bus is gone.
	viaBus := f.NewModule(t, "via-bus", c.bus, time.Hour)
	poller := f.NewModule(t, "poller", c.bus, 50*time.Millisecond)
	run(t, viaBus)
	run(t, poller)
	waitConsumer(t, c.srv, "via-bus")
	waitConsumer(t, c.srv, "poller")
	if v := bundleVersion(t, ctx, viaBus); v != 0 {
		t.Fatalf("initial version = %d", v)
	}
	bundleVersion(t, ctx, poller)

	obj, _, err := publisher.CreateDraft(ctx, KindFlow, "login", flow("true"), "")
	ok(t, err)
	_, err = publisher.Publish(ctx, []ids.ConfigObjectID{obj.ID}, "one")
	ok(t, err)
	if v := bundleVersion(t, ctx, publisher); v != 1 {
		t.Fatalf("the publisher reads %d after its own publish", v)
	}
	pollFor(t, "the notification to reach the other instance", 5*time.Second, func() bool { return bundleVersion(t, ctx, viaBus) == 1 })

	// The bus goes away: the poller still follows, the other instance keeps its last bundle.
	c.srv.Shutdown()
	obj2, _, err := publisher.CreateDraft(ctx, KindFlow, "second", flow("false"), "")
	ok(t, err)
	_, err = publisher.Publish(ctx, []ids.ConfigObjectID{obj2.ID}, "two")
	ok(t, err)
	pollFor(t, "polling to pick up the bundle without the bus", 5*time.Second, func() bool { return bundleVersion(t, ctx, poller) == 2 })
	if v := bundleVersion(t, ctx, viaBus); v != 1 {
		t.Fatalf("the instance without polling moved to %d with the bus down", v)
	}
}

// ---- a bundle that cannot load keeps the last good one in service

func staticStability(t *testing.T) {
	t.Helper()
	f := NewFixture(t, "config.bundles")
	m := f.NewModule(t, "stable", nil, 30*time.Millisecond)
	tn, env := f.Tenant(t), Environment(t)
	ctx := Ctx(t, tn, env)
	obj, _, err := m.CreateDraft(ctx, KindFlow, "login", flow("true"), "")
	ok(t, err)
	_, err = m.Publish(ctx, []ids.ConfigObjectID{obj.ID}, "")
	ok(t, err)
	run(t, m)
	if v := bundleVersion(t, ctx, m); v != 1 {
		t.Fatalf("version = %d", v)
	}

	// A newer bundle whose checksum does not match its payload.
	conn, err := pgx.Connect(t.Context(), f.Env.AdminURL.Reveal())
	ok(t, err)
	defer func() { _ = conn.Close(t.Context()) }()
	_, err = conn.Exec(t.Context(), `INSERT INTO config.bundles (tenant_id, environment_id, version, checksum, payload)
		SELECT tenant_id, environment_id, 2, 'sha256:bad', payload FROM config.bundles WHERE version = 1`)
	ok(t, err)
	pollFor(t, "a failed reload to be logged", 5*time.Second, func() bool { return f.Logs.Count("reload failed") > 0 })
	if v := bundleVersion(t, ctx, m); v != 1 {
		t.Fatalf("the corrupt bundle replaced the good one: version %d", v)
	}
	// A fresh process cannot load it, and says so.
	fresh := f.NewModule(t, "fresh", nil, time.Hour)
	if _, err := fresh.Bundle(ctx); err == nil {
		t.Fatal("a bundle with a wrong checksum loaded")
	}
	// The next publish repairs the sequence.
	pub, err := m.Publish(ctx, nil, "after")
	ok(t, err)
	if pub.BundleVersion != 3 {
		t.Fatalf("bundle version = %d", pub.BundleVersion)
	}
	pollFor(t, "the repaired bundle", 5*time.Second, func() bool { return bundleVersion(t, ctx, fresh) == 3 })
}

func construction(t *testing.T) {
	t.Helper()
	f := NewFixture(t)
	for name, d := range map[string]config.Deps{
		"no db":          {Auditor: f.Stack.Auditor, Registry: f.Registry, InstanceID: "x"},
		"no auditor":     {DB: f.Env.DB, Registry: f.Registry, InstanceID: "x"},
		"no registry":    {DB: f.Env.DB, Auditor: f.Stack.Auditor, InstanceID: "x"},
		"no instance ID": {DB: f.Env.DB, Auditor: f.Stack.Auditor, Registry: f.Registry},
	} {
		if _, err := config.New(d); !errs.Is(err, errs.CodeInternal) {
			t.Errorf("%s: New = %v, want an internal error", name, err)
		}
	}
	if err := config.RegisterEvents(f.Stack.Registry); err == nil {
		t.Error("registering the events twice succeeded")
	}
	if _, _, err := f.Module.CreateDraft(t.Context(), KindFlow, "x", flow("true"), ""); !errs.Is(err, errs.CodeInternal) {
		t.Errorf("without a tenant scope: %v", err)
	}
	if m := config.Migrations(); m.Schema != "config" {
		t.Errorf("migration schema = %q", m.Schema)
	}
}
