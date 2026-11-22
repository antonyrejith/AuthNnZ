package store_test

import (
	"testing"

	"github.com/antonyrejith/portun/internal/modules/audit/internal/store"
	"github.com/antonyrejith/portun/internal/platform/ids"
)

// TestRecordErrors covers malformed inputs of the hashing functions.
//
// Covers: GOV-18
func TestRecordErrors(t *testing.T) {
	t.Parallel()
	raw := marshal(t, sampleEvent(t, ids.TenantID{}))
	if _, err := store.Seal(raw, 1, "sha256:bad"); err == nil {
		t.Error("Seal accepted an invalid prev hash")
	}
	if _, err := store.Seal([]byte(`[`), 1, store.GenesisHash); err == nil {
		t.Error("Seal accepted invalid JSON")
	}
	if _, err := store.Canonical([]byte(`{`), 1); err == nil {
		t.Error("Canonical accepted invalid JSON")
	}
	tests := map[string]string{
		"not json":     `{`,
		"no integrity": string(raw),
		"bad prev":     `{"sequence":1,"integrity":{"prev_hash":"x","hash":"y"}}`,
		"bad sequence": `{"sequence":0,"integrity":{"prev_hash":"` + store.GenesisHash + `","hash":"y"}}`,
	}
	for name, in := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, _, err := store.Recompute([]byte(in)); err == nil {
				t.Error("Recompute succeeded")
			}
		})
	}
	var h store.Header
	if h.TargetType() != "" || h.TargetID() != "" || h.Rows() != nil {
		t.Error("empty header accessors")
	}
}
