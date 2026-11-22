package writer

import (
	"log/slog"
	"testing"

	"github.com/antonyrejith/portun/internal/platform/bus"
	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/ids"
	"github.com/antonyrejith/portun/internal/platform/secret"
)

// TestNewValidates requires the dependencies.
//
// Covers: GOV-01
func TestNewValidates(t *testing.T) {
	t.Parallel()
	if _, err := New(Config{}); !errs.Is(err, errs.CodeInvalidRequest) {
		t.Errorf("New = %v", err)
	}
	if _, err := New(Config{Bus: &bus.Bus{}, LockURL: secret.NewString("postgres://x")}); !errs.Is(err, errs.CodeInvalidRequest) {
		t.Errorf("New without DB = %v", err)
	}
}

// TestCheckSubject requires the record's chain to match its subject.
//
// Covers: GOV-01
func TestCheckSubject(t *testing.T) {
	t.Parallel()
	tn, err := ids.New[ids.TenantKind]()
	if err != nil {
		t.Fatal(err)
	}
	own, err := bus.AuditSubject(tn)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		subject string
		tenant  ids.TenantID
		ok      bool
	}{
		{"tenant", own, tn, true},
		{"platform", bus.AuditPlatformSubject, ids.TenantID{}, true},
		{"platform record on a tenant subject", own, ids.TenantID{}, false},
		{"tenant record on the platform subject", bus.AuditPlatformSubject, tn, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if err := checkSubject(tt.subject, tt.tenant); (err == nil) != tt.ok {
				t.Errorf("checkSubject = %v, want ok=%v", err, tt.ok)
			}
		})
	}
}

func discard() *slog.Logger { return slog.New(slog.DiscardHandler) }
