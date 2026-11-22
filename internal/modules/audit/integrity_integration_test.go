//go:build integration

package audit_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/antonyrejith/portun/internal/modules/audit"
	"github.com/antonyrejith/portun/internal/platform/db/dbtest"
	"github.com/antonyrejith/portun/internal/platform/ids"
	"github.com/antonyrejith/portun/internal/platform/secret"
)

// TestIntegrityOperations runs the operator verification and export entry
// points; each records the access in the platform trail (and the tenant's)
// before reading.
//
// Covers: GOV-16, GOV-18
func TestIntegrityOperations(t *testing.T) {
	t.Parallel()
	env := dbtest.New(t)
	op := audit.Operator{ID: "ops-alice", Reason: "quarterly review"}
	tn, err := ids.New[ids.TenantKind]()
	if err != nil {
		t.Fatal(err)
	}
	rep, err := audit.VerifyDatabase(t.Context(), env.PlatformURL, "platform", op, audit.VerifyOptions{})
	if err != nil || !rep.OK() || rep.Events != 1 {
		t.Fatalf("VerifyDatabase = %+v, %v", rep, err)
	}
	var buf bytes.Buffer
	if err := audit.ExportDatabase(t.Context(), env.PlatformURL, tn.String(), op, 0, 0, &buf); err != nil {
		t.Fatal(err)
	}
	rep, err = audit.VerifyExport(t.Context(), &buf, audit.VerifyOptions{})
	if err != nil || !rep.OK() || rep.Events != 1 {
		t.Fatalf("tenant export = %+v, %v", rep, err)
	}
	rep, err = audit.VerifyDatabase(t.Context(), env.PlatformURL, "platform", op, audit.VerifyOptions{})
	if err != nil || !rep.OK() || rep.Events != 3 {
		t.Fatalf("platform trail = %+v, %v", rep, err)
	}
	if pem, err := audit.SigningKeysPEM(t.Context(), env.PlatformURL); err != nil || len(pem) != 0 {
		t.Errorf("SigningKeysPEM = %q, %v", pem, err)
	}
	bad := []struct {
		name string
		err  func() error
	}{
		{"no operator", func() error {
			_, err := audit.VerifyDatabase(t.Context(), env.PlatformURL, "platform", audit.Operator{}, audit.VerifyOptions{})
			return err
		}},
		{"bad chain", func() error {
			_, err := audit.VerifyDatabase(t.Context(), env.PlatformURL, "nope", op, audit.VerifyOptions{})
			return err
		}},
		{"bad URL", func() error {
			_, err := audit.VerifyDatabase(t.Context(), secret.NewString("::"), "platform", op, audit.VerifyOptions{})
			return err
		}},
		{"application role cannot append", func() error {
			_, err := audit.VerifyDatabase(t.Context(), env.AppURL, "platform", op, audit.VerifyOptions{})
			return err
		}},
		{"keys with bad URL", func() error {
			_, err := audit.SigningKeysPEM(t.Context(), secret.NewString("::"))
			return err
		}},
		{"bad export", func() error {
			_, err := audit.VerifyExport(t.Context(), strings.NewReader("{"), audit.VerifyOptions{})
			return err
		}},
		{"bad trusted keys", func() error {
			_, err := audit.ParseTrustedKeys([]byte("x"))
			return err
		}},
		{"bad TSA roots", func() error {
			_, err := audit.ParseTSARoots([]byte("x"))
			return err
		}},
	}
	for _, b := range bad {
		if b.err() == nil {
			t.Errorf("%s: succeeded", b.name)
		}
	}
}
