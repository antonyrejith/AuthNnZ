package verify_test

import (
	"strings"
	"testing"
	"time"

	"github.com/antonyrejith/portun/internal/modules/audit/internal/verify"
)

// TestReadExportErrors rejects malformed export files.
//
// Covers: GOV-18
func TestReadExportErrors(t *testing.T) {
	t.Parallel()
	header := `{"kind":"header","format":"portun.audit.export.v1","chain":"00000000-0000-0000-0000-000000000000"}`
	tests := map[string]string{
		"empty":          "",
		"not json":       "{",
		"no header":      `{"kind":"event"}`,
		"wrong format":   `{"kind":"header","format":"x","chain":"00000000-0000-0000-0000-000000000000"}`,
		"two headers":    header + "\n" + header,
		"unknown kind":   header + "\n" + `{"kind":"other"}`,
		"event no event": header + "\n" + `{"kind":"event","sequence":1}`,
	}
	for name, in := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := verify.ReadExport(strings.NewReader(in)); err == nil {
				t.Error("ReadExport succeeded")
			}
		})
	}
}

// TestVerifyOptions covers the timestamp options and file time ranges.
//
// Covers: GOV-18
func TestVerifyOptions(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	m := f.build(t, 10, 5)
	lines := exportLines(t, m)
	// Without TSA roots the tokens are counted as unverified.
	rep := verifyLines(t, lines, verify.Options{})
	if !rep.OK() || rep.TSAUnverified != 2 || rep.TSAVerified != 0 || !rep.KeysFromSource {
		t.Fatalf("report = %+v", rep)
	}
	// A required timestamp that is missing is reported.
	cp := m.cps[0]
	cp.TSAToken, cp.TSATime = nil, nil
	rep = verifyLines(t, replaceCheckpoint(t, exportLines(t, m), cp), f.opts())
	if f := rep.First(); f == nil || f.Kind != verify.KindTSAMissing {
		t.Fatalf("report = %+v", rep)
	}
	// A stored timestamp time that differs from the token is reported.
	cp = m.cps[1]
	other := cp.TSATime.Add(time.Hour)
	cp.TSATime = &other
	rep = verifyLines(t, replaceCheckpoint(t, exportLines(t, m), cp), f.opts())
	if f := rep.First(); f == nil || f.Kind != verify.KindTSAInvalid {
		t.Fatalf("report = %+v", rep)
	}
	// Time ranges over a file.
	src, err := verify.ReadExport(strings.NewReader(strings.Join(lines, "\n")))
	if err != nil {
		t.Fatal(err)
	}
	opts := f.opts()
	opts.From, opts.To = f.start.Add(6*time.Second), f.start.Add(7*time.Second)
	rep, err = verify.Chain(t.Context(), src, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK() || rep.FromSeq != 6 || rep.ToSeq != 10 {
		t.Fatalf("time range report = %+v", rep)
	}
	opts.From, opts.To = f.start.Add(time.Hour), time.Time{}
	if rep, err = verify.Chain(t.Context(), src, opts); err != nil || rep.Events != 0 {
		t.Fatalf("empty range report = %+v, %v", rep, err)
	}
}
