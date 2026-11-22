// Package dbtest is the PostgreSQL kit for integration tests (ADR-0017). It
// is compiled only with the integration build tag.
//
// One PostgreSQL container runs per test binary. Main starts it, creates the
// roles, migrates a template database once, runs the tests and removes the
// container. New gives each test a fresh database cloned from the template:
//
//	func TestMain(m *testing.M) {
//		os.Exit(dbtest.Main(m, dbtest.Options{Sets: []migrate.Set{{Schema: "identity", FS: migrations}}}))
//	}
//
//	func TestSomething(t *testing.T) {
//		t.Parallel()
//		env := dbtest.New(t)
//		// env.DB, env.OwnerURL, env.AppURL, env.PlatformURL, env.AdminURL
//	}
//
// The container runs the image pinned by digest with logical replication
// enabled (wal_level=logical), so change-capture tests can use it too.
//
// Container cleanup does not use the Testcontainers reaper (Ryuk): version
// v0.45.0 pulls it by tag only, which the pinning policy (D-15) forbids. Main
// disables it, terminates the container itself after the tests and on
// SIGINT/SIGTERM, and starts PostgreSQL under timeout(1) so that a container
// left behind by a killed test binary stops (and is auto-removed) after
// MaxLifetime at the latest.
package dbtest
