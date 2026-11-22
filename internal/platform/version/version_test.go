package version_test

import (
	"runtime"
	"testing"

	"github.com/antonyrejith/portun/internal/platform/version"
)

func TestGet(t *testing.T) {
	t.Parallel()
	got := version.Get()
	if got.Version == "" || got.Commit == "" || got.GoVersion != runtime.Version() {
		t.Fatalf("Get() = %+v", got)
	}
}
