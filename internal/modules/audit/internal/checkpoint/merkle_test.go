package checkpoint_test

import (
	"bytes"
	"crypto/x509"
	"encoding/hex"
	"testing"

	"pgregory.net/rapid"

	"github.com/antonyrejith/portun/internal/modules/audit/internal/checkpoint"
	"github.com/antonyrejith/portun/internal/modules/audit/internal/checkpoint/tsatest"
	"github.com/antonyrejith/portun/internal/platform/errs"
)

// ctLeaves are the leaf inputs of the Certificate Transparency reference
// Merkle tree tests.
func ctLeaves(t *testing.T) [][]byte {
	t.Helper()
	var out [][]byte
	for _, h := range []string{"", "00", "10", "2021", "3031", "40414243", "5051525354555657", "606162636465666768696a6b6c6d6e6f"} {
		b, err := hex.DecodeString(h)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, b)
	}
	return out
}

// TestRootHashVectors checks RFC 6962 tree hashes against the reference
// vectors.
//
// Covers: GOV-18
func TestRootHashVectors(t *testing.T) {
	t.Parallel()
	roots := []string{
		"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		"6e340b9cffb37a989ca544e6bb780a2c78901d3fb33738768511a30617afa01d",
		"fac54203e7cc696cf0dfcb42c92a1d9dbaf70ad9e621f4bd8d98662f00e3c125",
		"aeb6bcfe274b70a14fb067a5e5578264db0fa9b51af5e0ba159158f329e06e77",
		"d37ee418976dd95753c1c73862b9398fa2a2cf9b4ff0fdfe8b30cd95209614b7",
		"4e3bbb1f7b478dcfe71fb631631519a3bca12c9aefca1612bfce4c13a86264d4",
		"76e67dadbcdf1e10e1b74ddc608abd2f98dfb16fbce75277b5232a127f2087ef",
		"ddb89be403809e325750d3d263cd78929c2942b7942a34b77e122c9594a74c8c",
		"5dc9da79a70659a9ad559cb701ded9a2ab9d823aad2f4960cfe370eff4604328",
	}
	leaves := ctLeaves(t)
	for n, want := range roots {
		if got := hex.EncodeToString(checkpoint.RootHash(leaves[:n])); got != want {
			t.Errorf("root of %d leaves = %s, want %s", n, got, want)
		}
	}
}

// TestInclusionProofProperty checks that every leaf of every tree proves
// against the root, and that a proof never verifies another leaf, index,
// size or root.
//
// Covers: GOV-18
func TestInclusionProofProperty(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(rt *rapid.T) {
		n := rapid.IntRange(1, 70).Draw(rt, "n")
		items := make([][]byte, n)
		for i := range items {
			items[i] = rapid.SliceOfN(rapid.Byte(), 32, 32).Draw(rt, "item")
		}
		root := checkpoint.RootHash(items)
		idx := rapid.IntRange(0, n-1).Draw(rt, "index")
		proof, err := checkpoint.InclusionProof(items, idx)
		if err != nil {
			rt.Fatal(err)
		}
		if !checkpoint.VerifyInclusion(items[idx], uint64(idx), uint64(n), proof, root) {
			rt.Fatalf("proof of leaf %d of %d does not verify", idx, n)
		}
		other := (idx + 1) % n
		if n > 1 && !bytes.Equal(items[other], items[idx]) && checkpoint.VerifyInclusion(items[other], uint64(idx), uint64(n), proof, root) {
			rt.Fatal("proof verified another leaf")
		}
		if n > 1 && checkpoint.VerifyInclusion(items[idx], uint64(other), uint64(n), proof, root) && !bytes.Equal(items[other], items[idx]) {
			rt.Fatal("proof verified at another index")
		}
		bad := append([]byte(nil), root...)
		bad[0] ^= 1
		if checkpoint.VerifyInclusion(items[idx], uint64(idx), uint64(n), proof, bad) {
			rt.Fatal("proof verified against another root")
		}
		if checkpoint.VerifyInclusion(items[idx], uint64(idx), uint64(n+1), proof, root) && n > 1 {
			// Some sizes share paths for the first leaves; the root differs then.
			if bytes.Equal(checkpoint.RootHash(append(items, items[0])), root) {
				rt.Fatal("proof verified against another size")
			}
		}
	})
	if _, err := checkpoint.InclusionProof([][]byte{{1}}, 1); !errs.Is(err, errs.CodeInvalidRequest) {
		t.Errorf("out-of-range index: %v", err)
	}
}

// TestTSA timestamps through the RFC 3161 client and verifies the tokens.
//
// Covers: GOV-18
func TestTSA(t *testing.T) {
	t.Parallel()
	tsa := tsatest.Start(t)
	data := []byte("signature bytes")
	tok, err := checkpoint.HTTPStamper{URL: tsa.URL, Roots: tsa.Roots}.Timestamp(t.Context(), data)
	if err != nil {
		t.Fatalf("Timestamp: %v", err)
	}
	if tok.Time.IsZero() || len(tok.DER) == 0 {
		t.Fatal("empty token")
	}
	other := tsatest.Start(t)
	tests := []struct {
		name  string
		data  []byte
		roots *x509.CertPool
		ok    bool
	}{
		{"valid", data, tsa.Roots, true},
		{"other data", []byte("other"), tsa.Roots, false},
		{"untrusted authority", data, other.Roots, false},
		{"no roots", data, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := checkpoint.VerifyToken(tok.DER, tt.data, tt.roots)
			if (err == nil) != tt.ok {
				t.Fatalf("VerifyToken err = %v, want ok=%v", err, tt.ok)
			}
			if tt.ok && !got.Equal(tok.Time) {
				t.Errorf("time = %v, want %v", got, tok.Time)
			}
		})
	}
	corrupt := append([]byte(nil), tok.DER...)
	corrupt[len(corrupt)-5] ^= 0xff
	if _, err := checkpoint.VerifyToken(corrupt, data, tsa.Roots); err == nil {
		t.Error("a corrupted token verified")
	}
	tsa.Fail.Store(true)
	if _, err := (checkpoint.HTTPStamper{URL: tsa.URL}).Timestamp(t.Context(), data); !errs.Is(err, errs.CodeUnavailable) {
		t.Errorf("failing TSA: %v", err)
	}
	if _, err := (checkpoint.HTTPStamper{URL: other.URL, Roots: tsa.Roots}).Timestamp(t.Context(), data); err == nil {
		t.Error("a token from an untrusted TSA was accepted")
	}
}
