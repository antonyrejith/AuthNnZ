package migrate

import (
	"context"

	"github.com/antonyrejith/portun/internal/platform/secret"
)

// EnsureLoginRole exposes ensureRoles with a custom role name, so tests can
// create roles without changing the shared portun_app and portun_platform.
func EnsureLoginRole(ctx context.Context, ownerURL secret.String, name string, bypassRLS bool, password secret.String) error {
	return ensureRoles(ctx, ownerURL, []roleSpec{{name: name, bypassRLS: bypassRLS, password: password}})
}
