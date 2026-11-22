package retention

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/antonyrejith/portun/internal/platform/errs"
)

// TestHoldOrMap maps the store's legal-hold refusal.
//
// Covers: GOV-01
func TestHoldOrMap(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		code errs.Code
	}{
		{"legal hold", &pgconn.PgError{Code: "55000", Message: "audit chain x is under legal hold"}, errs.CodePreconditionFailed},
		{"other prerequisite", &pgconn.PgError{Code: "55000", Message: "retention must end at a checkpoint boundary"}, errs.CodeInternal},
		{"unique", &pgconn.PgError{Code: "23505"}, errs.CodeConflict},
		{"plain", errors.New("boom"), errs.CodeInternal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := errs.CodeOf(holdOrMap(tt.err)); got != tt.code {
				t.Errorf("code = %s, want %s", got, tt.code)
			}
		})
	}
}

// TestNewValidates requires the dependencies.
//
// Covers: GOV-01
func TestNewValidates(t *testing.T) {
	t.Parallel()
	if _, err := New(Config{}); !errs.Is(err, errs.CodeInvalidRequest) {
		t.Errorf("New = %v", err)
	}
	if err := (&Service{}).SetPolicy(t.Context(), Policy{RetentionDays: -1}); !errs.Is(err, errs.CodeValidationFailed) {
		t.Errorf("SetPolicy = %v", err)
	}
}
