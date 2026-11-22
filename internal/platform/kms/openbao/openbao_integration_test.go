//go:build integration

package openbao_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/antonyrejith/portun/internal/platform/crypto"
	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/kms"
	"github.com/antonyrejith/portun/internal/platform/kms/kmstest"
	"github.com/antonyrejith/portun/internal/platform/kms/openbao"
	"github.com/antonyrejith/portun/internal/platform/secret"
)

const (
	image = "openbao/openbao:2.4.4@sha256:595c83b42614a4d2b044608e4593c05b019c5db25bc9c185d8fff3ac96c03ddd"
	// maxLifetime stops the server, and AutoRemove deletes the container,
	// even if this test binary dies before terminating it.
	maxLifetime = "900"
)

// server is the OpenBao dev server shared by the tests in this binary.
var server struct {
	addr  string
	token secret.String
}

// TestMain starts one OpenBao dev server per test binary. Ryuk (the
// testcontainers reaper) is disabled: the container is terminated explicitly,
// and its server stops itself after maxLifetime with AutoRemove set, so a
// crashed run cannot leak it for long.
func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	if err := os.Setenv("TESTCONTAINERS_RYUK_DISABLED", "true"); err != nil {
		fmt.Fprintln(os.Stderr, "openbao test setup:", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	token, err := crypto.RandomToken(24)
	if err != nil {
		fmt.Fprintln(os.Stderr, "openbao test setup:", err)
		return 1
	}
	ctr, err := testcontainers.Run(ctx, image,
		testcontainers.WithEntrypoint("timeout", maxLifetime, "bao"),
		testcontainers.WithCmd("server", "-dev", "-dev-root-token-id="+token, "-dev-listen-address=0.0.0.0:8200"),
		testcontainers.WithEnv(map[string]string{"SKIP_SETCAP": "true"}),
		testcontainers.WithExposedPorts("8200/tcp"),
		testcontainers.WithHostConfigModifier(func(hc *container.HostConfig) { hc.AutoRemove = true }),
		testcontainers.WithWaitStrategy(wait.ForHTTP("/v1/sys/health").WithPort("8200/tcp").WithStartupTimeout(time.Minute)),
	)
	defer func() {
		if err := testcontainers.TerminateContainer(ctr); err != nil {
			fmt.Fprintln(os.Stderr, "terminate OpenBao container:", err)
		}
	}()
	if err != nil {
		fmt.Fprintln(os.Stderr, "start OpenBao:", err)
		return 1
	}
	endpoint, err := ctr.PortEndpoint(ctx, "8200/tcp", "http")
	if err != nil {
		fmt.Fprintln(os.Stderr, "OpenBao endpoint:", err)
		return 1
	}
	server.addr, server.token = endpoint, secret.NewString(token)
	for _, mount := range []struct {
		path   string
		config bool
	}{{"transit", true}, {"portun/transit", true}, {"upsert", false}, {"flip", true}} {
		if err := setupMount(ctx, mount.path, mount.config); err != nil {
			fmt.Fprintln(os.Stderr, "set up transit mount:", err)
			return 1
		}
	}
	return m.Run()
}

// setupMount enables a transit mount and, if harden is set, disables upsert.
func setupMount(ctx context.Context, path string, harden bool) error {
	if err := admin(ctx, "sys/mounts/"+path, map[string]any{"type": "transit"}); err != nil {
		return err
	}
	if !harden {
		return nil
	}
	return admin(ctx, path+"/config/keys", map[string]any{"disable_upsert": true})
}

func admin(ctx context.Context, path string, body any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.addr+"/v1/"+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("X-Vault-Token", server.token.Reveal())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("POST %s: HTTP %d", path, resp.StatusCode)
	}
	return nil
}

func newKMS(t *testing.T, mount string) *openbao.KMS {
	t.Helper()
	k, err := openbao.New(openbao.Config{
		Address: server.addr, Token: server.token, Mount: mount,
		Timeout: 10 * time.Second, AllowInsecureHTTP: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// TestContract runs the KMS contract against OpenBao transit.
//
// Covers: SEC-08
func TestContract(t *testing.T) {
	t.Parallel()
	kmstest.Run(t, func(t *testing.T) kms.KMS {
		t.Helper()
		return newKMS(t, "transit")
	})
}

// TestContractNestedMount runs the contract against a nested mount path.
//
// Covers: SEC-08
func TestContractNestedMount(t *testing.T) {
	t.Parallel()
	kmstest.Run(t, func(t *testing.T) kms.KMS {
		t.Helper()
		return newKMS(t, "portun/transit")
	})
}

// TestDestroyedAcrossInstances checks a second process sees a destroyed key
// as destroyed (the tombstone is in OpenBao, not in process memory).
//
// Covers: SEC-08
func TestDestroyedAcrossInstances(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	a, b := newKMS(t, "transit"), newKMS(t, "transit")
	ref, err := a.CreateKey(ctx, kmstest.UniqueName(t, "x"), kms.PurposeEncrypt)
	if err != nil {
		t.Fatal(err)
	}
	ct, err := b.Encrypt(ctx, ref, []byte("data"), nil) // b caches the purpose
	if err != nil {
		t.Fatal(err)
	}
	if err := a.DestroyKey(ctx, ref); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Decrypt(ctx, ref, ct, nil); errs.CodeOf(err) != errs.CodeNotFound || !strings.Contains(err.Error(), kms.ErrKeyDestroyed.Error()) {
		t.Fatalf("Decrypt in another instance after destroy: %v", err)
	}
	if _, err := b.Encrypt(ctx, ref, []byte("new"), nil); errs.CodeOf(err) != errs.CodeNotFound {
		t.Fatalf("Encrypt after destroy must not recreate the key: %v", err)
	}
}

// TestRefusesUpsertMount checks Encrypt fails closed on a mount that would
// silently create missing keys.
//
// Covers: SEC-08
func TestRefusesUpsertMount(t *testing.T) {
	t.Parallel()
	k := newKMS(t, "upsert")
	ref, err := k.CreateKey(t.Context(), kmstest.UniqueName(t, "u"), kms.PurposeEncrypt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.Encrypt(t.Context(), ref, []byte("x"), nil); errs.CodeOf(err) != errs.CodeInternal || !strings.Contains(err.Error(), "disable_upsert") {
		t.Fatalf("Encrypt on an upsert mount: %v, want refusal", err)
	}
	if err := newKMS(t, "transit").Check(t.Context()); err != nil {
		t.Fatalf("Check on a hardened mount: %v", err)
	}
}

// TestConcurrentCreateKey proves that of several concurrent CreateKey calls
// for one name exactly one succeeds and the others get CodeConflict, for the
// same and for different purposes.
//
// Covers: SEC-08
func TestConcurrentCreateKey(t *testing.T) {
	t.Parallel()
	for _, mixed := range []bool{false, true} {
		t.Run(fmt.Sprintf("mixed purposes %v", mixed), func(t *testing.T) {
			t.Parallel()
			name := kmstest.UniqueName(t, "c")
			const callers = 8
			results := make(chan error, callers)
			var start sync.WaitGroup
			start.Add(1)
			for i := range callers {
				purpose := kms.PurposeEncrypt
				if mixed && i%2 == 1 {
					purpose = kms.PurposeSign
				}
				go func() {
					start.Wait()
					_, err := newKMS(t, "transit").CreateKey(context.Background(), name, purpose)
					results <- err
				}()
			}
			start.Done()
			won := 0
			for range callers {
				switch err := <-results; {
				case err == nil:
					won++
				case errs.CodeOf(err) != errs.CodeConflict:
					t.Errorf("CreateKey: %v, want success or conflict", err)
				}
			}
			if won != 1 {
				t.Fatalf("%d callers created the key, want exactly 1", won)
			}
		})
	}
}

// TestEncryptNoticesUpsertEnabledAtRuntime proves that a mount reconfigured
// to allow upsert after startup is refused on the next key-cache miss.
//
// Covers: SEC-08
func TestEncryptNoticesUpsertEnabledAtRuntime(t *testing.T) {
	t.Parallel()
	k := newKMS(t, "flip")
	a, err := k.CreateKey(t.Context(), kmstest.UniqueName(t, "a"), kms.PurposeEncrypt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.Encrypt(t.Context(), a, []byte("x"), nil); err != nil {
		t.Fatal(err)
	}
	b, err := newKMS(t, "flip").CreateKey(t.Context(), kmstest.UniqueName(t, "b"), kms.PurposeEncrypt)
	if err != nil {
		t.Fatal(err)
	}
	if err := admin(t.Context(), "flip/config/keys", map[string]any{"disable_upsert": false}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin(context.Background(), "flip/config/keys", map[string]any{"disable_upsert": true}) })
	if _, err := k.Encrypt(t.Context(), b, []byte("x"), nil); errs.CodeOf(err) != errs.CodeInternal || !strings.Contains(err.Error(), "disable_upsert") {
		t.Fatalf("Encrypt after upsert was enabled: %v, want refusal", err)
	}
}

// TestWrongToken checks a bad token maps to permission denied and is never
// echoed.
//
// Covers: SEC-08
func TestWrongToken(t *testing.T) {
	t.Parallel()
	const bad = "s.not-the-token-0123456789"
	k, err := openbao.New(openbao.Config{
		Address: server.addr, Token: secret.NewString(bad), Mount: "transit",
		Timeout: 5 * time.Second, AllowInsecureHTTP: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = k.CreateKey(t.Context(), kmstest.UniqueName(t, "w"), kms.PurposeEncrypt)
	if errs.CodeOf(err) != errs.CodePermissionDenied {
		t.Fatalf("CreateKey with a bad token: %v", err)
	}
	if strings.Contains(err.Error(), bad) || strings.Contains(err.Error(), server.token.Reveal()) {
		t.Fatal("error contains a token")
	}
}
