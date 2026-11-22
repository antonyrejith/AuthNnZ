//go:build integration

package tenancy_test

import (
	"os"
	"testing"

	"github.com/antonyrejith/portun/internal/modules/tenancy/tenancytest"
)

func TestMain(m *testing.M) { os.Exit(tenancytest.Main(m)) }

// TestTenancy runs the module's scenario suite against the facade: tenant
// lifecycle with crypto-shredding, hierarchy, isolation, hosts, pagination,
// export and the middleware, each followed by change-capture reconciliation.
//
// Covers: TEN-01, TEN-03, TEN-09, GOV-11, GOV-12
func TestTenancy(t *testing.T) {
	t.Parallel()
	tenancytest.RunSuite(t)
}
