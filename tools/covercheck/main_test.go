package main

import (
	"strings"
	"testing"
)

const sample = `ok  	example.com/m/internal/platform/crypto	0.5s	coverage: 95.0% of statements
ok  	example.com/m/internal/platform/kms/local	(cached)	coverage: 87.2% of statements
ok  	example.com/m/internal/platform/log	0.1s	coverage: 81.0% of statements
	example.com/m/internal/platform/db/dbtest		coverage: 0.0% of statements
?   	example.com/m/internal/platform/version/x	[no test files]
ok  	example.com/m/internal/platform/ids	0.1s	coverage: 79.9% of statements
`

func TestCheck(t *testing.T) {
	t.Parallel()
	cfg := config{
		min: 80, strictMin: 90,
		strict:  split("internal/platform/crypto,internal/platform/kms"),
		exclude: split("internal/platform/db/dbtest"),
	}
	tests := []struct {
		name    string
		input   string
		wantOK  bool
		wantBad []string
	}{
		{"mixed", sample, false, []string{"kms/local", "version/x", "ids"}},
		{"all good", "ok  \texample.com/m/a\t0.1s\tcoverage: 100.0% of statements\n", true, nil},
		{"test failure", "FAIL\texample.com/m/a\t0.1s\n", false, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			res, err := check(strings.NewReader(tt.input), cfg)
			if err != nil {
				t.Fatal(err)
			}
			if res.ok() != tt.wantOK {
				t.Fatalf("ok() = %v, want %v\n%s", res.ok(), tt.wantOK, res.report())
			}
			report := res.report()
			for _, bad := range tt.wantBad {
				found := false
				for _, l := range strings.Split(report, "\n") {
					if strings.Contains(l, bad) && strings.Contains(l, "FAIL") {
						found = true
					}
				}
				if !found {
					t.Errorf("report does not fail %s:\n%s", bad, report)
				}
			}
			if strings.Contains(report, "dbtest") {
				t.Errorf("excluded package reported:\n%s", report)
			}
		})
	}
}
