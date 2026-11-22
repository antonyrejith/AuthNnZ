//go:build integration

package dbtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/moby/moby/api/types/container"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/antonyrejith/portun/internal/platform/db"
	"github.com/antonyrejith/portun/internal/platform/db/migrate"
	"github.com/antonyrejith/portun/internal/platform/secret"
)

// Image is the PostgreSQL image, pinned by digest (D-15).
const Image = "postgres:19.0@sha256:bb6e70add742cd868fb62cb3b2f67d1dd2bdd2b65a521e778420748753687c60"

// MaxLifetime is the longest a container lives, even if the test binary is
// killed before it can remove the container.
const MaxLifetime = 2 * time.Hour

const (
	// OwnerRole owns the template and test databases and runs migrations.
	// Like a production owner role, it is not a superuser.
	OwnerRole = "portun_owner"

	adminUser      = "postgres"
	templateName   = "portun_template"
	startTimeout   = 3 * time.Minute
	cleanupTimeout = time.Minute
)

// Options configures Main.
type Options struct {
	// Sets are applied, in order, to the template database.
	Sets []migrate.Set
}

// Env is one test's database.
type Env struct {
	// DB is open on this database as the application and platform roles.
	DB *db.DB
	// OwnerURL connects as OwnerRole, which owns the schemas and tables.
	OwnerURL secret.String
	// AppURL connects as db.AppRole.
	AppURL secret.String
	// PlatformURL connects as db.PlatformRole.
	PlatformURL secret.String
	// AdminURL connects as the superuser, for test-only operations
	// (replication slots, inspecting or breaking things on purpose).
	AdminURL secret.String
	// Name is the database name.
	Name string
}

// instance is the running container of this test binary.
type instance struct {
	ctr       testcontainers.Container
	hostPort  string
	passwords map[string]string // role name -> password
	admin     *pgxpool.Pool     // superuser on the maintenance database
	stopOnce  sync.Once
}

// current is the instance Main started; New reads it. A test binary has one
// TestMain, so there is exactly one.
var current atomic.Pointer[instance] //nolint:gochecknoglobals // the test binary's single container, set once by Main and read by New

// Main starts PostgreSQL, prepares the template database, runs the tests and
// removes the container. It returns the exit code for os.Exit.
func Main(m *testing.M, opts Options) int {
	// Never pull the unpinned reaper image (see the package documentation).
	if err := os.Setenv("TESTCONTAINERS_RYUK_DISABLED", "true"); err != nil {
		fmt.Fprintln(os.Stderr, "dbtest: disable the reaper:", err)
		return 1
	}
	inst, err := start(opts)
	if inst != nil {
		defer inst.stop()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "dbtest: start PostgreSQL:", err)
		return 1
	}
	current.Store(inst)
	stopSignals := stopOnSignal(inst)
	defer stopSignals()
	return m.Run()
}

// stopOnSignal removes the container when the test binary is interrupted.
func stopOnSignal(inst *instance) func() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		select {
		case sig := <-ch:
			inst.stop()
			fmt.Fprintln(os.Stderr, "dbtest: stopped PostgreSQL after", sig)
			os.Exit(1)
		case <-done:
		}
	}()
	return func() {
		signal.Stop(ch)
		close(done)
	}
}

func start(opts Options) (*instance, error) {
	ctx, cancel := context.WithTimeout(context.Background(), startTimeout)
	defer cancel()
	inst := &instance{passwords: map[string]string{}}
	for _, role := range []string{adminUser, OwnerRole, db.AppRole, db.PlatformRole} {
		pw, err := randomHex(16)
		if err != nil {
			return nil, err
		}
		inst.passwords[role] = pw
	}
	ctr, err := postgres.Run(ctx, Image,
		postgres.WithUsername(adminUser),
		postgres.WithPassword(inst.passwords[adminUser]),
		postgres.WithDatabase("postgres"),
		testcontainers.WithEntrypoint("timeout", "-s", "INT", "-k", "30s", strconv.Itoa(int(MaxLifetime.Seconds()))+"s", "docker-entrypoint.sh"),
		testcontainers.WithCmd("postgres",
			"-c", "fsync=off",
			"-c", "synchronous_commit=off",
			"-c", "full_page_writes=off",
			"-c", "wal_level=logical",
			"-c", "max_replication_slots=20",
			"-c", "max_wal_senders=20",
			"-c", "max_connections=300",
		),
		testcontainers.WithLabels(map[string]string{"io.portun.dbtest": "true"}),
		testcontainers.WithHostConfigModifier(func(hc *container.HostConfig) { hc.AutoRemove = true }),
		postgres.BasicWaitStrategies(),
	)
	if ctr != nil {
		inst.ctr = ctr
	}
	if err != nil {
		return inst, fmt.Errorf("run container: %w", err)
	}
	if inst.hostPort, err = ctr.PortEndpoint(ctx, "5432/tcp", ""); err != nil {
		return inst, fmt.Errorf("container endpoint: %w", err)
	}
	if err := inst.prepare(ctx, opts); err != nil {
		return inst, err
	}
	return inst, nil
}

// prepare creates the roles and the migrated template database.
func (i *instance) prepare(ctx context.Context, opts Options) error {
	adminURL := i.url(adminUser, "postgres")
	admin, err := pgxpool.New(ctx, adminURL.Reveal())
	if err != nil {
		return fmt.Errorf("admin pool: %w", err)
	}
	i.admin = admin
	if err := migrate.EnsureRoles(ctx, adminURL,
		secret.NewString(i.passwords[db.AppRole]), secret.NewString(i.passwords[db.PlatformRole])); err != nil {
		return fmt.Errorf("ensure roles: %w", err)
	}
	// The password is random hex, so it cannot end the literal.
	stmts := []string{
		"CREATE ROLE " + ident(OwnerRole) + " WITH LOGIN NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE PASSWORD '" + i.passwords[OwnerRole] + "'",
		"CREATE DATABASE " + ident(templateName) + " OWNER " + ident(OwnerRole),
	}
	for _, s := range stmts {
		if _, err := admin.Exec(ctx, s); err != nil {
			return fmt.Errorf("prepare template: %w", err)
		}
	}
	if len(opts.Sets) > 0 {
		if err := migrate.Up(ctx, i.url(OwnerRole, templateName), opts.Sets...); err != nil {
			return fmt.Errorf("migrate template: %w", err)
		}
	}
	// Nobody may connect to the template: a session on it would block cloning.
	if _, err := admin.Exec(ctx, "ALTER DATABASE "+ident(templateName)+" WITH IS_TEMPLATE true ALLOW_CONNECTIONS false"); err != nil {
		return fmt.Errorf("mark template: %w", err)
	}
	return nil
}

// stop closes the admin pool and removes the container. It is safe to call
// more than once and from several goroutines.
func (i *instance) stop() {
	i.stopOnce.Do(func() {
		if i.admin != nil {
			i.admin.Close()
		}
		if i.ctr == nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancel()
		// The container is auto-removed once stopped, so Terminate's own
		// removal can find it gone or already being removed.
		if err := i.ctr.Terminate(ctx); err != nil && !cerrdefs.IsNotFound(err) && !cerrdefs.IsConflict(err) {
			fmt.Fprintln(os.Stderr, "dbtest: remove the PostgreSQL container:", err)
		}
	})
}

// url returns a connection URL for role on database name.
func (i *instance) url(role, name string) secret.String {
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(role, i.passwords[role]),
		Host:     i.hostPort,
		Path:     "/" + name,
		RawQuery: "sslmode=disable",
	}
	return secret.NewString(u.String())
}

// New returns a fresh database cloned from the migrated template, with
// env.DB open on it. The database is dropped when the test ends. Safe for
// parallel tests.
func New(tb testing.TB) *Env {
	tb.Helper()
	inst := current.Load()
	if inst == nil {
		tb.Fatal("dbtest: New needs dbtest.Main in TestMain")
		return nil
	}
	suffix, err := randomHex(8)
	if err != nil {
		tb.Fatal(err)
	}
	name := "t_" + suffix
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	if _, err := inst.admin.Exec(ctx, "CREATE DATABASE "+ident(name)+" TEMPLATE "+ident(templateName)+" OWNER "+ident(OwnerRole)); err != nil {
		tb.Fatalf("dbtest: clone the template: %v", err)
	}
	tb.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancel()
		if _, err := inst.admin.Exec(ctx, "DROP DATABASE IF EXISTS "+ident(name)+" WITH (FORCE)"); err != nil {
			tb.Errorf("dbtest: drop database %s: %v", name, err)
		}
	})
	env := &Env{
		OwnerURL:    inst.url(OwnerRole, name),
		AppURL:      inst.url(db.AppRole, name),
		PlatformURL: inst.url(db.PlatformRole, name),
		AdminURL:    inst.url(adminUser, name),
		Name:        name,
	}
	d, err := db.Open(ctx, db.Config{AppURL: env.AppURL, PlatformURL: env.PlatformURL, MaxConns: 4, PlatformMaxConns: 2})
	if err != nil {
		tb.Fatalf("dbtest: open database: %v", err)
	}
	tb.Cleanup(d.Close) // runs before the drop
	env.DB = d
	return env
}

func ident(s string) string { return pgx.Identifier{s}.Sanitize() }

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", errors.Join(errors.New("dbtest: random bytes"), err)
	}
	return hex.EncodeToString(b), nil
}
