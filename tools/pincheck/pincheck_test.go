package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const digest = "sha256:aafe993665bf6200db38a02c11a9351e782a84b6a45e7f33b6695638c88e3931"

func TestCheckers(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		check     checker
		input     string
		wantCount int
		wantText  string
	}{
		{"go.mod with toolchain", CheckGoMod, "module x\n\ngo 1.27.0\n\ntoolchain go1.27.4\n", 0, ""},
		{"go.mod without toolchain", CheckGoMod, "module x\n\ngo 1.27.0\n", 1, "toolchain"},
		{"package.json exact", CheckPackageJSON, `{"dependencies":{"react":"19.3.1"},"devDependencies":{"vite":"8.3.8"}}`, 0, ""},
		{"package.json caret", CheckPackageJSON, `{"dependencies":{"react":"^19.3.1"}}`, 1, `"^19.3.1"`},
		{"package.json tilde and star", CheckPackageJSON, `{"dependencies":{"a":"~1.0.0","b":"*"}}`, 2, "not an exact version"},
		{"package.json workspace", CheckPackageJSON, `{"dependencies":{"@portun/ui":"workspace:*"}}`, 0, ""},
		{"package.json packageManager exact", CheckPackageJSON, `{"packageManager":"pnpm@12.21.0"}`, 0, ""},
		{"package.json packageManager missing version", CheckPackageJSON, `{"packageManager":"pnpm"}`, 1, "packageManager"},
		{"Dockerfile pinned", CheckDockerfile, "# syntax=docker/dockerfile:1.29.0@" + digest + "\nFROM golang:1.27.4-trixie@" + digest + " AS build\nFROM build AS test\nFROM gcr.io/distroless/static-debian13:nonroot@" + digest + "\n", 0, ""},
		{"Dockerfile unpinned base", CheckDockerfile, "FROM golang:1.27.4-trixie AS build\n", 1, "pinned by digest"},
		{"Dockerfile unpinned syntax", CheckDockerfile, "# syntax=docker/dockerfile:1\nFROM scratch\n", 1, "syntax frontend"},
		{"Dockerfile platform flag", CheckDockerfile, "FROM --platform=$BUILDPLATFORM golang:1.27.4@" + digest + " AS build\n", 0, ""},
		{"Dockerfile latest in RUN", CheckDockerfile, "FROM scratch\nRUN go install example.com/x@latest\n", 1, "latest"},
		{"compose pinned", CheckYAML, "services:\n  db:\n    image: postgres:19.0@" + digest + "\n", 0, ""},
		{"compose unpinned", CheckYAML, "services:\n  db:\n    image: postgres:19.0\n", 1, "postgres:19.0"},
		{"compose latest", CheckYAML, "services:\n  db:\n    image: postgres:latest\n", 1, "pinned by digest"},
		{"helm template image skipped", CheckYAML, "image: \"{{ .Values.image }}\"\n", 0, ""},
		{"workflow pinned", CheckWorkflow, "steps:\n  - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1\n  - uses: ./.github/actions/x\n", 0, ""},
		{"workflow tag", CheckWorkflow, "steps:\n  - uses: actions/checkout@v7\n", 1, "full commit SHA"},
		{"workflow docker unpinned", CheckWorkflow, "steps:\n  - uses: docker://alpine:3\n", 1, "digest"},
		{"terraform exact", CheckTerraform, "required_version = \"1.13.9\"\nversion = \"= 6.20.0\"\n", 0, ""},
		{"terraform range", CheckTerraform, "version = \"~> 6.0\"\nrequired_version = \">= 1.13\"\n", 2, "one exact version"},
		{"script latest", CheckScript, "go install golang.org/x/vuln/cmd/govulncheck@latest\n", 1, "latest"},
		{"script comment latest ignored", CheckScript, "# never use @latest here\n", 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := tt.check("f", strings.NewReader(tt.input))
			if err != nil {
				t.Fatalf("check: %v", err)
			}
			if len(got) != tt.wantCount {
				t.Fatalf("got %d violations %v, want %d", len(got), got, tt.wantCount)
			}
			if tt.wantText != "" && !strings.Contains(got[0].Message, tt.wantText) {
				t.Errorf("message %q does not contain %q", got[0].Message, tt.wantText)
			}
		})
	}
}

func TestScanSkipsDocsAndFindsViolations(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module x\n\ngo 1.27.0\n\ntoolchain go1.27.4\n")
	write("docs/example/compose.yaml", "services:\n  a:\n    image: postgres:19.0\n")
	write("deploy/compose/compose.yaml", "services:\n  a:\n    image: postgres:19.0\n")
	write(".github/workflows/ci.yml", "jobs:\n  a:\n    steps:\n      - uses: actions/checkout@v7\n")

	got, err := Scan(root)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %v, want 2 violations (compose and workflow, docs skipped)", got)
	}
	if got[0].File != filepath.Join(".github", "workflows", "ci.yml") || got[1].File != filepath.Join("deploy", "compose", "compose.yaml") {
		t.Errorf("unexpected files: %v", got)
	}
}
