package migrate

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/secret"
)

// TestWrap checks the error mapping of migration failures: platform errors
// pass through, transient database errors keep their code, anything else is
// internal.
func TestWrap(t *testing.T) {
	t.Parallel()
	platform := errs.New(errs.CodeConflict, "x")
	tests := []struct {
		name string
		err  error
		want errs.Code
	}{
		{"platform error passes through", fmt.Errorf("w: %w", platform), errs.CodeConflict},
		{"deadline", context.DeadlineExceeded, errs.CodeDeadlineExceeded},
		{"other", errors.New("syntax error"), errs.CodeInternal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := errs.CodeOf(wrap(tt.err, "migrate: step")); got != tt.want {
				t.Fatalf("wrap(%v) code = %s, want %s", tt.err, got, tt.want)
			}
		})
	}
}

// TestUnreachableDatabase: Up and EnsureRoles report an unreachable server
// as unavailable and a malformed URL as invalid, without the password.
func TestUnreachableDatabase(t *testing.T) {
	t.Parallel()
	const unreachable = "postgres://u:secretpw@127.0.0.1:1/x?connect_timeout=2"
	const malformed = "postgres://u:secretpw@host:notaport/x"
	pw := secret.NewString("a-long-enough-password-1")
	tests := []struct {
		name string
		run  func() error
		want errs.Code
	}{
		{"Up unreachable", func() error {
			return Up(context.Background(), nil, secret.NewString(unreachable), Set{Schema: "a", FS: fstest.MapFS{}})
		}, errs.CodeUnavailable},
		{"EnsureRoles unreachable", func() error {
			return EnsureRoles(context.Background(), secret.NewString(unreachable), pw, pw)
		}, errs.CodeUnavailable},
		{"EnsureRoles malformed", func() error {
			return EnsureRoles(context.Background(), secret.NewString(malformed), pw, pw)
		}, errs.CodeInvalidRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.run()
			if !errs.Is(err, tt.want) {
				t.Fatalf("err = %v, want %s", err, tt.want)
			}
			if strings.Contains(err.Error(), "secretpw") {
				t.Fatal("error leaks the password")
			}
		})
	}
}
