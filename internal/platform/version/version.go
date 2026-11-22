// Package version reports the build version, set at build time with
// -ldflags "-X github.com/antonyrejith/portun/internal/platform/version.version=…".
package version

import "runtime"

// Set at build time through -ldflags -X; these must be package variables.
var (
	version = "dev"
	commit  = "unknown" //nolint:gochecknoglobals // set at build time with -ldflags -X
)

// Info describes the running build.
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	GoVersion string `json:"go_version"`
}

// Get returns the build information.
func Get() Info {
	return Info{Version: version, Commit: commit, GoVersion: runtime.Version()}
}
