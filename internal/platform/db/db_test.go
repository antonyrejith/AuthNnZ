package db

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/puddle/v2"

	"github.com/antonyrejith/portun/internal/platform/config"
	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/secret"
)

// TestConfigDefaultsMatchTags keeps the env-tag defaults and the code
// defaults (applied to zero fields) in step.
func TestConfigDefaultsMatchTags(t *testing.T) {
	t.Parallel()
	env := map[string]string{"DB_APP_URL": "postgres://a@h/d", "DB_PLATFORM_URL": "postgres://p@h/d"}
	var loaded Config
	if err := config.Load("DB_", &loaded, func(k string) (string, bool) { v, ok := env[k]; return v, ok }); err != nil {
		t.Fatal(err)
	}
	coded := Config{AppURL: loaded.AppURL, PlatformURL: loaded.PlatformURL}.withDefaults()
	secrets := cmp.Comparer(func(a, b secret.String) bool { return a.Equal(b) })
	if diff := cmp.Diff(loaded, coded, secrets); diff != "" {
		t.Fatalf("tag defaults differ from code defaults (-tags +code):\n%s", diff)
	}
	if !cmp.Equal(loaded.withDefaults(), loaded, secrets) {
		t.Fatal("withDefaults changed a fully loaded config")
	}
}

func TestConfigLoadRequiresURLs(t *testing.T) {
	t.Parallel()
	var c Config
	err := config.Load("DB_", &c, func(string) (string, bool) { return "", false })
	if !errs.Is(err, errs.CodeInvalidRequest) || !strings.Contains(err.Error(), "DB_APP_URL") || !strings.Contains(err.Error(), "DB_PLATFORM_URL") {
		t.Fatalf("err = %v, want both URLs required", err)
	}
}

func TestConfigValidate(t *testing.T) {
	t.Parallel()
	urls := Config{AppURL: secret.NewString("postgres://a@h/d"), PlatformURL: secret.NewString("postgres://p@h/d")}
	with := func(f func(*Config)) Config { c := urls; f(&c); return c }
	tests := []struct {
		name string
		cfg  Config
		want []string // field pointers; nil: valid
	}{
		{"urls only", urls, nil},
		{"missing urls", Config{}, []string{"/app_url", "/platform_url"}},
		{"negative pool", with(func(c *Config) { c.MaxConns = -1; c.PlatformMaxConns = -1 }), []string{"/max_conns", "/platform_max_conns"}},
		{"negative timeout", with(func(c *Config) { c.StatementTimeout = -time.Second }), []string{"/statement_timeout"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.cfg.Validate()
			if tt.want == nil {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			_, _, fields := errs.Public(err)
			got := make([]string, 0, len(fields))
			for _, f := range fields {
				got = append(got, f.Pointer)
			}
			if !errs.Is(err, errs.CodeInvalidRequest) || !cmp.Equal(got, tt.want) {
				t.Fatalf("err = %v, fields %v; want %v", err, got, tt.want)
			}
		})
	}
}

// TestPoolConfig checks the server settings every connection gets and that a
// bad URL never leaks into the error.
//
// Covers: SEC-09
func TestPoolConfig(t *testing.T) {
	t.Parallel()
	cfg := Config{StatementTimeout: 1500 * time.Millisecond}.withDefaults()
	pc, err := poolConfig("application", secret.NewString("postgres://u:pw@h:5432/d?application_name=custom"), 7, cfg)
	if err != nil {
		t.Fatal(err)
	}
	rp := pc.ConnConfig.RuntimeParams
	if pc.MaxConns != 7 || rp["statement_timeout"] != "1500" || rp["idle_in_transaction_session_timeout"] != "60000" ||
		rp["application_name"] != "custom" || pc.ConnConfig.ConnectTimeout != 5*time.Second {
		t.Fatalf("pool config: max %d, params %v, connect timeout %v", pc.MaxConns, rp, pc.ConnConfig.ConnectTimeout)
	}
	_, err = poolConfig("application", secret.NewString("postgres://u:topsecret@h:notaport/d"), 1, cfg)
	if !errs.Is(err, errs.CodeInvalidRequest) || strings.Contains(err.Error(), "topsecret") {
		t.Fatalf("err = %v, want CodeInvalidRequest without the password", err)
	}
}

func TestOpenValidatesBeforeConnecting(t *testing.T) {
	t.Parallel()
	_, err := Open(context.Background(), Config{})
	if !errs.Is(err, errs.CodeInvalidRequest) {
		t.Fatalf("err = %v, want CodeInvalidRequest", err)
	}
}

func TestTxOptions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		opts []TxOption
		want pgx.TxOptions
	}{
		{"default", nil, pgx.TxOptions{IsoLevel: pgx.ReadCommitted, AccessMode: pgx.ReadWrite}},
		{"read only", []TxOption{ReadOnly()}, pgx.TxOptions{IsoLevel: pgx.ReadCommitted, AccessMode: pgx.ReadOnly}},
		{"serializable", []TxOption{Serializable()}, pgx.TxOptions{IsoLevel: pgx.Serializable, AccessMode: pgx.ReadWrite}},
		{"repeatable read", []TxOption{RepeatableRead()}, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadWrite}},
		{"combined", []TxOption{Serializable(), ReadOnly()}, pgx.TxOptions{IsoLevel: pgx.Serializable, AccessMode: pgx.ReadOnly}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := txOptions(tt.opts); got != tt.want {
				t.Fatalf("txOptions = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestMapError(t *testing.T) {
	t.Parallel()
	pgErr := func(code string) error {
		return fmt.Errorf("exec: %w", &pgconn.PgError{Code: code, Message: `duplicate key value violates "users_email_key"`, Detail: "Key (email)=(a@b.c) already exists."})
	}
	passthrough := errs.New(errs.CodeNotFound, "user not found")
	tests := []struct {
		name      string
		err       error
		want      errs.Code
		retryable bool
	}{
		{"unique", pgErr("23505"), errs.CodeConflict, false},
		{"exclusion", pgErr("23P01"), errs.CodeConflict, false},
		{"foreign key", pgErr("23503"), errs.CodeConflict, false},
		{"serialization", pgErr("40001"), errs.CodeConflict, true},
		{"deadlock", pgErr("40P01"), errs.CodeConflict, true},
		{"statement timeout", pgErr("57014"), errs.CodeDeadlineExceeded, false},
		{"idle in tx timeout", pgErr("25P03"), errs.CodeDeadlineExceeded, false},
		{"lock timeout", pgErr("55P03"), errs.CodeUnavailable, false},
		{"too many connections", pgErr("53300"), errs.CodeUnavailable, false},
		{"connection failure", pgErr("08006"), errs.CodeUnavailable, false},
		{"admin shutdown", pgErr("57P01"), errs.CodeUnavailable, false},
		{"rls violation", pgErr("42501"), errs.CodeInternal, false},
		{"syntax", pgErr("42601"), errs.CodeInternal, false},
		{"check", pgErr("23514"), errs.CodeInternal, false},
		{"no rows", fmt.Errorf("scan: %w", pgx.ErrNoRows), errs.CodeNotFound, false},
		{"deadline", fmt.Errorf("q: %w", context.DeadlineExceeded), errs.CodeDeadlineExceeded, false},
		{"canceled", context.Canceled, errs.CodeUnavailable, false},
		{"closed pool", puddle.ErrClosedPool, errs.CodeUnavailable, false},
		{"conn closed", fmt.Errorf("x: %w", pgconn.ErrConnClosed), errs.CodeUnavailable, false},
		{"eof", io.ErrUnexpectedEOF, errs.CodeUnavailable, false},
		{"tx closed", pgx.ErrTxClosed, errs.CodeInternal, false},
		{"plain", errors.New("boom"), errs.CodeInternal, false},
		{"passthrough", passthrough, errs.CodeNotFound, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := MapError(tt.err)
			if !errs.Is(got, tt.want) {
				t.Fatalf("MapError(%v) = %v, want code %s", tt.err, got, tt.want)
			}
			if !errors.Is(got, tt.err) {
				t.Fatal("the original error is not kept as the cause")
			}
			if Retryable(got) != tt.retryable {
				t.Fatalf("Retryable = %v, want %v", !tt.retryable, tt.retryable)
			}
			if _, msg, _ := errs.Public(got); strings.Contains(msg, "users_email_key") || strings.Contains(msg, "a@b.c") {
				t.Fatalf("client message %q leaks database details", msg)
			}
		})
	}
	if MapError(nil) != nil {
		t.Fatal("MapError(nil) != nil")
	}
	if MapError(passthrough) != passthrough { //nolint:errorlint // identity is the point: the same error must come back
		t.Fatal("an *errs.Error was rewrapped")
	}
}

// TestRoleProblems checks the role rules Open enforces.
//
// Covers: SEC-09
func TestRoleProblems(t *testing.T) {
	t.Parallel()
	safeApp := roleFacts{name: "portun_app"}
	safePlatform := roleFacts{name: "portun_platform", bypassRLS: true}
	with := func(f roleFacts, mod func(*roleFacts)) roleFacts { mod(&f); return f }
	tests := []struct {
		name     string
		app      roleFacts
		platform roleFacts
		want     []string
	}{
		{"safe", safeApp, safePlatform, nil},
		{"app superuser", with(safeApp, func(f *roleFacts) { f.superuser = true }), safePlatform, []string{"is a superuser"}},
		{"app bypassrls", with(safeApp, func(f *roleFacts) { f.bypassRLS = true }), safePlatform, []string{"has BYPASSRLS"}},
		{"app member of superuser", with(safeApp, func(f *roleFacts) { f.memberOfSuperuser = true }), safePlatform, []string{"member of a superuser"}},
		{"app member of bypassrls", with(safeApp, func(f *roleFacts) { f.memberOfBypassRLS = true }), safePlatform, []string{"member of a role with BYPASSRLS"}},
		{"app owns database", with(safeApp, func(f *roleFacts) { f.ownsDatabase = true }), safePlatform, []string{"owns the database"}},
		{"app owns tables", with(safeApp, func(f *roleFacts) { f.ownsTables = true }), safePlatform, []string{"owns tables"}},
		{"app default tenant", with(safeApp, func(f *roleFacts) { f.tenantSetting = "x" }), safePlatform, []string{"default tenant"}},
		{"platform no bypassrls", safeApp, with(safePlatform, func(f *roleFacts) { f.bypassRLS = false }), []string{"lacks BYPASSRLS"}},
		{"platform superuser", safeApp, with(safePlatform, func(f *roleFacts) { f.superuser = true }), []string{"is a superuser"}},
		{"platform owns tables", safeApp, with(safePlatform, func(f *roleFacts) { f.ownsTables = true }), []string{"owns tables"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := append(appRoleProblems(tt.app), platformRoleProblems(tt.platform)...)
			if len(got) != len(tt.want) {
				t.Fatalf("problems = %q, want %d", got, len(tt.want))
			}
			for i := range got {
				if !strings.Contains(got[i], tt.want[i]) {
					t.Fatalf("problem %q lacks %q", got[i], tt.want[i])
				}
			}
		})
	}
}
