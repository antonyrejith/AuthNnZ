//go:build integration

package migrate_test

import (
	"testing"
	"testing/fstest"

	"github.com/antonyrejith/portun/internal/platform/db/dbtest"
	"github.com/antonyrejith/portun/internal/platform/db/migrate"
	"github.com/antonyrejith/portun/internal/platform/errs"
)

// TestUpFailures checks that a failing migration run reports an error with
// a code, for every step that can fail on a live database.
func TestUpFailures(t *testing.T) {
	t.Parallel()
	good := fstest.MapFS{"00001_t.sql": {Data: []byte("-- +goose Up\nCREATE TABLE fail_ok.t (id int);\n")}}
	badSQL := fstest.MapFS{"00001_bad.sql": {Data: []byte("-- +goose Up\nTHIS IS NOT SQL;\n")}}
	dupVersion := fstest.MapFS{
		"00001_a.sql": {Data: []byte("-- +goose Up\nSELECT 1;\n")},
		"00001_b.sql": {Data: []byte("-- +goose Up\nSELECT 1;\n")},
	}
	tests := []struct {
		name string
		app  bool // connect as the application role, which may not create schemas
		set  migrate.Set
	}{
		{"schema cannot be created", true, migrate.Set{Schema: "fail_ok", FS: good}},
		{"invalid migration set", false, migrate.Set{Schema: "fail_dup", FS: dupVersion}},
		{"migration statement fails", false, migrate.Set{Schema: "fail_sql", FS: badSQL}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			env := dbtest.New(t)
			url := env.OwnerURL
			if tt.app {
				url = env.AppURL
			}
			err := migrate.Up(t.Context(), nil, url, tt.set)
			if err == nil {
				t.Fatal("Up succeeded")
			}
			if code := errs.CodeOf(err); !code.Known() {
				t.Fatalf("Up error %v has no platform code", err)
			}
		})
	}
}
