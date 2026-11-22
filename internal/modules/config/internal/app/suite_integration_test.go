//go:build integration

package app_test

import (
	"testing"

	"github.com/antonyrejith/portun/internal/modules/config/configtest"
)

func TestMain(m *testing.M) { configtest.Main(m) }

// TestConfigSuite runs the module's scenario suite: versioned configuration
// (draft, validate, publish, rollback, immutability), CEL evaluation from
// bundles, settings inheritance with modes, policies and resets, platform
// settings, isolation, concurrent publishing and bundle propagation over the
// bus and by polling, each followed by change-capture reconciliation.
//
// Covers: DYN-01, DYN-05, TEN-06, GOV-11, GOV-12
func TestConfigSuite(t *testing.T) {
	t.Parallel()
	configtest.RunSuite(t)
}
