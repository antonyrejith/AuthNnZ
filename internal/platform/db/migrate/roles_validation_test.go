package migrate

import (
	"context"
	"testing"

	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/secret"
)

// TestEnsureRolesRejectsBadPasswordsBeforeConnecting: an unacceptable
// password for either role is refused as an invalid request before any
// connection is attempted (the URL below is unreachable, so a connection
// attempt would be reported as unavailable instead).
//
// Covers: SEC-09
func TestEnsureRolesRejectsBadPasswordsBeforeConnecting(t *testing.T) {
	t.Parallel()
	const unreachable = "postgres://u:x@127.0.0.1:1/x?connect_timeout=1"
	good := secret.NewString("a-long-enough-password-1")
	tests := []struct {
		name          string
		app, platform secret.String
	}{
		{"short app password", secret.NewString("short"), good},
		{"short platform password", good, secret.NewString("short")},
		{"non-printable platform password", good, secret.NewString("password\x00password\x00")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := EnsureRoles(context.Background(), secret.NewString(unreachable), tt.app, tt.platform)
			if !errs.Is(err, errs.CodeInvalidRequest) {
				t.Fatalf("err = %v, want invalid request", err)
			}
		})
	}
}
