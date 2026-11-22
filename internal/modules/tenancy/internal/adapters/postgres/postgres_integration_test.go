//go:build integration

package postgres_test

import (
	"os"
	"testing"

	"github.com/antonyrejith/portun/internal/modules/tenancy/tenancytest"
)

func TestMain(m *testing.M) { os.Exit(tenancytest.Main(m)) }

// TestTenancy runs the module's scenario suite against the PostgreSQL
// adapter, so that its SQL is measured by the same scenarios as the facade.
//
// Covers: TEN-01, TEN-03, TEN-09, GOV-11, GOV-12
func TestTenancy(t *testing.T) {
	t.Parallel()
	tenancytest.RunSuite(t)
}
