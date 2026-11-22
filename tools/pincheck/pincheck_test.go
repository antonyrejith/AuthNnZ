package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	digest = "sha256:aafe993665bf6200db38a02c11a9351e782a84b6a45e7f33b6695638c88e3931"
	sha    = "3d3c42e5aac5ba805825da76410c181273ba90b1"
	pinned = "postgres:19.0@" + digest
)

type checkCase struct {
	name      string
	check     checker
	input     string
	wantCount int
	wantText  string
}

func runCases(t *testing.T, tests []checkCase) {
	t.Helper()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := tt.check("f", []byte(tt.input))
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

func TestGoFiles(t *testing.T) {
	t.Parallel()
	runCases(t, []checkCase{
		{"go.mod with toolchain", CheckGoMod, "module x\n\ngo 1.27.0\n\ntoolchain go1.27.4\n", 0, ""},
		{"go.mod without toolchain", CheckGoMod, "module x\n\ngo 1.27.0\n", 1, "toolchain"},
		{"go.work with toolchain", CheckGoWork, "go 1.27.0\n\ntoolchain go1.27.4\n\nuse .\n", 0, ""},
		{"go.work without toolchain", CheckGoWork, "go 1.27.0\n\nuse .\n", 1, "go.work must pin"},
	})
}

func TestPackageJSON(t *testing.T) {
	t.Parallel()
	runCases(t, []checkCase{
		{"exact", CheckPackageJSON, `{"dependencies":{"react":"19.3.1"},"devDependencies":{"vite":"8.3.8"}}`, 0, ""},
		{"caret", CheckPackageJSON, `{"dependencies":{"react":"^19.3.1"}}`, 1, `"^19.3.1"`},
		{"tilde and star", CheckPackageJSON, `{"dependencies":{"a":"~1.0.0","b":"*"}}`, 2, "not an exact version"},
		{"workspace", CheckPackageJSON, `{"dependencies":{"@portun/ui":"workspace:*"}}`, 0, ""},
		{"npm alias exact", CheckPackageJSON, `{"dependencies":{"x":"npm:@scope/real@1.2.3","y":"npm:real@4.5.6"}}`, 0, ""},
		{"npm alias range", CheckPackageJSON, `{"dependencies":{"x":"npm:real@^1.2.3"}}`, 1, "npm:real@^1.2.3"},
		{"overrides nested", CheckPackageJSON, `{"overrides":{"a":"1.0.0","b":{".":"2.0.0","c":"^3.0.0"},"d":"$a"}}`, 1, `overrides.b "c"`},
		{"resolutions", CheckPackageJSON, `{"resolutions":{"**/lodash":"^4.17.21","left-pad":"1.3.0"}}`, 1, "resolutions"},
		{"pnpm overrides", CheckPackageJSON, `{"pnpm":{"overrides":{"foo@<2":">=2.1.0","bar":"2.0.0"}}}`, 1, "pnpm.overrides"},
		{"packageManager exact", CheckPackageJSON, `{"packageManager":"pnpm@12.21.0"}`, 0, ""},
		{"packageManager missing version", CheckPackageJSON, `{"packageManager":"pnpm"}`, 1, "packageManager"},
	})
}

func TestDockerfile(t *testing.T) {
	t.Parallel()
	runCases(t, []checkCase{
		{"pinned", CheckDockerfile, "# syntax=docker/dockerfile:1.29.0@" + digest + "\nFROM golang:1.27.4-trixie@" + digest + " AS build\nFROM build AS test\nFROM gcr.io/distroless/static-debian13:nonroot@" + digest + "\n", 0, ""},
		{"unpinned base", CheckDockerfile, "FROM golang:1.27.4-trixie AS build\n", 1, "tag and digest"},
		{"digest without tag", CheckDockerfile, "FROM golang@" + digest + "\n", 1, "must carry a tag"},
		{"unpinned syntax", CheckDockerfile, "# syntax=docker/dockerfile:1\nFROM scratch\n", 1, "syntax frontend"},
		{"syntax variant spacing and case", CheckDockerfile, "#  Syntax = docker/dockerfile:1\nFROM scratch\n", 1, "syntax frontend"},
		{"syntax without space", CheckDockerfile, "#syntax=docker/dockerfile:1.29.0\nFROM scratch\n", 1, "syntax frontend"},
		{"platform flag", CheckDockerfile, "FROM --platform=$BUILDPLATFORM golang:1.27.4@" + digest + " AS build\n", 0, ""},
		{"latest in RUN", CheckDockerfile, "FROM scratch\nRUN go install example.com/x@latest\n", 1, "latest"},
		{"go install branch in RUN", CheckDockerfile, "FROM scratch\nRUN go install example.com/x@master\n", 1, "exact version"},
		{"COPY --from image", CheckDockerfile, "FROM scratch\nCOPY --from=alpine:3.22 /bin/sh /sh\n", 1, "COPY --from"},
		{"COPY --from stage and index", CheckDockerfile, "FROM " + pinned + " AS build\nFROM scratch\nCOPY --from=build /a /a\nCOPY --from=0 /b /b\n", 0, ""},
		{"COPY --from pinned image", CheckDockerfile, "FROM scratch\nCOPY --from=" + pinned + " /a /a\n", 0, ""},
		{"RUN mount from image", CheckDockerfile, "FROM scratch\nRUN --mount=type=bind,from=alpine:3.22,target=/x true\n", 1, "--mount from"},
		{"RUN mount from stage", CheckDockerfile, "FROM " + pinned + " AS deps\nFROM scratch\nRUN --mount=type=cache,from=deps,target=/x true\n", 0, ""},
		{"ARG default unpinned", CheckDockerfile, "ARG BASE=golang:1.27.4\nFROM ${BASE}\n", 1, "golang:1.27.4"},
		{"ARG default pinned", CheckDockerfile, "ARG BASE=" + pinned + "\nFROM ${BASE} AS b\nFROM $BASE\n", 0, ""},
		{"ARG without default", CheckDockerfile, "ARG BASE\nFROM ${BASE}\n", 1, "without a pinned default"},
		{"ARG tag part", CheckDockerfile, "ARG V=19.0\nFROM postgres:${V}@" + digest + "\n", 0, ""},
		{"FROM continuation names image", CheckDockerfile, "FROM \\\n  golang:1.27.4 \\\n  AS build\n", 1, `"golang:1.27.4"`},
		{"latest tag", CheckDockerfile, "FROM postgres:latest@" + digest + "\n", 1, "latest"},
		{"curl pipe sh", CheckDockerfile, "FROM scratch\nRUN curl -fsSL https://example.com/install.sh | sh\n", 1, "piping a download"},
	})
}

func TestYAML(t *testing.T) {
	t.Parallel()
	runCases(t, []checkCase{
		{"compose pinned", CheckYAML, "services:\n  db:\n    image: " + pinned + "\n", 0, ""},
		{"compose unpinned", CheckYAML, "services:\n  db:\n    image: postgres:19.0\n", 1, "postgres:19.0"},
		{"compose latest", CheckYAML, "services:\n  db:\n    image: postgres:latest\n", 1, "latest"},
		{"digest without tag", CheckYAML, "image: postgres@" + digest + "\n", 1, "must carry a tag"},
		{"flow mapping", CheckYAML, "services: {db: {image: \"postgres:19.0\"}}\n", 1, "postgres:19.0"},
		{"value on next line", CheckYAML, "services:\n  db:\n    image:\n      postgres:19.0\n", 1, "postgres:19.0"},
		{"helm template image skipped", CheckYAML, "image: \"{{ .Values.image }}\"\n", 0, ""},
		{"helm template file", CheckYAML, "spec:\n  {{- if .Values.x }}\n  image: postgres:19.0\n  {{- end }}\n", 1, "postgres:19.0"},
		{"repository tag digest pinned", CheckYAML, "image:\n  repository: ghcr.io/x/portun\n  tag: 1.2.3\n  digest: " + digest + "\n", 0, ""},
		{"repository tag latest", CheckYAML, "image:\n  repository: ghcr.io/x/portun\n  tag: latest\n  digest: " + digest + "\n", 1, "exact tag"},
		{"repository missing digest", CheckYAML, "postgresql:\n  image:\n    registry: docker.io\n    repository: bitnami/postgresql\n    tag: 17.2.0\n", 1, "digest"},
		{"repository triple outside image key", CheckYAML, "worker:\n  repository: ghcr.io/x/w\n  tag: 1.0.0\n", 1, "digest"},
		{"compose dockerfile_inline", CheckYAML, "services:\n  a:\n    build:\n      dockerfile_inline: |\n        FROM alpine:3.22\n", 1, "alpine:3.22"},
		{"argo targetRevision HEAD", CheckYAML, "spec:\n  source:\n    targetRevision: HEAD\n", 1, "targetRevision"},
		{"argo targetRevision branch", CheckYAML, "spec:\n  source:\n    targetRevision: main\n", 1, "targetRevision"},
		{"argo targetRevision tag and sha", CheckYAML, "a:\n  targetRevision: v1.2.3\nb:\n  targetRevision: " + sha + "\n", 0, ""},
		{"kustomize image without digest", CheckYAML, "images:\n  - name: portun\n    newTag: 1.2.3\n", 1, "kustomize"},
		{"run script go install", CheckYAML, "pre-commit:\n  commands:\n    x:\n      run: go run example.com/tool@v1.2\n", 1, "exact version"},
	})
}

func TestChart(t *testing.T) {
	t.Parallel()
	runCases(t, []checkCase{
		{"exact dependency", CheckChart, "apiVersion: v2\nname: x\nversion: 0.1.0\ndependencies:\n  - name: pg\n    version: 16.2.1\n    repository: oci://r\n", 0, ""},
		{"range dependency", CheckChart, "apiVersion: v2\ndependencies:\n  - name: pg\n    version: ~16.2.0\n    repository: oci://r\n", 1, "chart dependency \"pg\""},
		{"missing version", CheckChart, "dependencies:\n  - name: pg\n    repository: oci://r\n", 1, "exact version"},
	})
}

func TestWorkflowAndAction(t *testing.T) {
	t.Parallel()
	runCases(t, []checkCase{
		{"workflow pinned", CheckWorkflow, "steps:\n  - uses: actions/checkout@" + sha + " # v7.0.1\n  - uses: ./.github/actions/x\n", 0, ""},
		{"workflow tag", CheckWorkflow, "steps:\n  - uses: actions/checkout@v7\n", 1, "full commit SHA"},
		{"workflow docker unpinned", CheckWorkflow, "steps:\n  - uses: docker://alpine:3\n", 1, "tag and digest"},
		{"workflow flow mapping uses", CheckWorkflow, "steps: [{uses: actions/checkout@v7}]\n", 1, "full commit SHA"},
		{"container scalar", CheckWorkflow, "jobs:\n  a:\n    container: node:24\n", 1, "job container"},
		{"container mapping", CheckWorkflow, "jobs:\n  a:\n    container:\n      image: node:24\n", 1, "node:24"},
		{"services image", CheckWorkflow, "jobs:\n  a:\n    services:\n      pg:\n        image: postgres:19.0\n", 1, "postgres:19.0"},
		{"runs-on latest", CheckWorkflow, "jobs:\n  a:\n    runs-on: ubuntu-latest\n", 1, "runner"},
		{"runs-on list latest", CheckWorkflow, "jobs:\n  a:\n    runs-on: [self-hosted, ubuntu-latest]\n", 1, "runner"},
		{"runs-on pinned", CheckWorkflow, "jobs:\n  a:\n    runs-on: ubuntu-24.04\n", 0, ""},
		{"run step curl pipe bash", CheckWorkflow, "steps:\n  - run: |\n      curl -sSL https://x.example/i | bash\n", 1, "piping a download"},
		{"composite action uses", CheckAction, "runs:\n  using: composite\n  steps:\n    - uses: actions/setup-go@v6\n", 1, "full commit SHA"},
		{"docker action Dockerfile", CheckAction, "runs:\n  using: docker\n  image: Dockerfile\n", 0, ""},
		{"docker action image", CheckAction, "runs:\n  using: docker\n  image: docker://alpine:3.22\n", 1, "tag and digest"},
	})
}

func TestTerraform(t *testing.T) {
	t.Parallel()
	runCases(t, []checkCase{
		{"exact", CheckTerraform, "terraform {\n  required_version = \"1.13.9\"\n  required_providers {\n    aws = {\n      source  = \"hashicorp/aws\"\n      version = \"= 6.20.0\"\n    }\n  }\n}\n", 0, ""},
		{"range", CheckTerraform, "terraform {\n  required_version = \">= 1.13\"\n  required_providers {\n    aws = {\n      source  = \"hashicorp/aws\"\n      version = \"~> 6.0\"\n    }\n  }\n}\n", 2, "one exact version"},
		{"inline object", CheckTerraform, "terraform {\n  required_providers {\n    aws = { source = \"hashicorp/aws\", version = \"~> 6.0\" }\n    k8s = { source = \"hashicorp/kubernetes\", version = \"2.38.0\" }\n  }\n}\n", 1, `"aws"`},
		{"provider without version", CheckTerraform, "terraform {\n  required_providers {\n    aws = { source = \"hashicorp/aws\" }\n  }\n}\n", 1, "missing"},
		{"legacy string provider", CheckTerraform, "terraform {\n  required_providers {\n    aws = \">= 5\"\n  }\n}\n", 1, "one exact version"},
		{"registry module without version", CheckTerraform, "module \"vpc\" {\n  source = \"terraform-aws-modules/vpc/aws\"\n}\n", 1, "registry module"},
		{"registry module range", CheckTerraform, "module \"vpc\" {\n  source  = \"terraform-aws-modules/vpc/aws\"\n  version = \"~> 6.0\"\n}\n", 1, "one exact version"},
		{"registry module exact", CheckTerraform, "module \"vpc\" {\n  source  = \"terraform-aws-modules/vpc/aws\"\n  version = \"6.5.0\"\n}\n", 0, ""},
		{"git module tag", CheckTerraform, "module \"x\" {\n  source = \"git::https://example.com/m.git?ref=v1.2.0\"\n}\n", 1, "commit SHA"},
		{"git module sha", CheckTerraform, "module \"x\" {\n  source = \"git::https://example.com/m.git//sub?ref=" + sha + "\"\n}\n", 0, ""},
		{"github shorthand", CheckTerraform, "module \"x\" {\n  source = \"github.com/org/repo\"\n}\n", 1, "commit SHA"},
		{"local module", CheckTerraform, "module \"x\" {\n  source = \"../modules/x\"\n}\n", 0, ""},
		{"resource version ignored", CheckTerraform, "# comment { \nresource \"aws_eks_cluster\" \"c\" {\n  version = \"1.33\"\n  tags = { a = \"b\" }\n}\n", 0, ""},
		{"heredoc and comments", CheckTerraform, "locals {\n  x = <<EOF\n{ not hcl\nEOF\n}\n/* module \"y\" { source = \"a/b/c\" } */\nmodule \"z\" {\n  source = \"a/b/c\"\n}\n", 1, `"z"`},
		{"json exact", CheckTerraformJSON, `{"terraform":{"required_version":"1.13.9","required_providers":{"aws":{"source":"hashicorp/aws","version":"6.20.0"}}}}`, 0, ""},
		{"json range", CheckTerraformJSON, `{"terraform":[{"required_providers":[{"aws":{"source":"hashicorp/aws","version":"~> 6"}}]}],"module":{"vpc":{"source":"terraform-aws-modules/vpc/aws"}}}`, 2, ""},
	})
}

func TestScript(t *testing.T) {
	t.Parallel()
	runCases(t, []checkCase{
		{"latest", CheckScript, "go install golang.org/x/vuln/cmd/govulncheck@latest\n", 1, "latest"},
		{"comment latest ignored", CheckScript, "# never use @latest here\n", 0, ""},
		{"go install exact", CheckScript, "go install golang.org/x/vuln/cmd/govulncheck@v1.10.0\n", 0, ""},
		{"go install branch", CheckScript, "go install example.com/x@master\n", 1, "exact version"},
		{"go run short version", CheckScript, "\tgo run example.com/x@v1.2 --flag a@b\n", 1, "exact version"},
		{"go install tool", CheckScript, "\tGOBIN=$(BIN) go -C tools install tool\n", 0, ""},
		{"docker run unpinned", CheckScript, "docker run --rm -it -v $(PWD):/src -e A=b alpine:3.22 sh\n", 1, "alpine:3.22"},
		{"docker run pinned via variable", CheckScript, "IMG := " + pinned + "\nx:\n\tdocker run --rm $(IMG) true\n", 0, ""},
		{"docker run unknown variable", CheckScript, "x:\n\tdocker run --rm $(NOPE) true\n", 1, "not defined"},
		{"npm global without version", CheckScript, "npm install -g pnpm\n", 1, "pnpm"},
		{"npm global exact", CheckScript, "npm i --global pnpm@10.12.1 @scope/cli@1.0.0\n", 0, ""},
		{"npm project install ignored", CheckScript, "npm install\n", 0, ""},
		{"curl pipe sh", CheckScript, "curl -fsSL https://example.com/install | sudo -E bash -s --\n", 1, "piping a download"},
		{"continuation", CheckScript, "go install \\\n  example.com/x@main\n", 1, "exact version"},
	})
}

func TestDevcontainer(t *testing.T) {
	t.Parallel()
	runCases(t, []checkCase{
		{"pinned", CheckDevcontainer, "{\n  // comment\n  \"image\": \"golang:1.27.4-trixie@" + digest + "\",\n  \"features\": {\"ghcr.io/devcontainers/features/docker-in-docker:2.12.2\": {}, \"ghcr.io/x/y@" + digest + "\": {}},\n  \"postCreateCommand\": \"make tools\",\n}\n", 0, ""},
		{"image without digest", CheckDevcontainer, `{"image":"golang:1.27.4"}`, 1, "devcontainer image"},
		{"feature major only", CheckDevcontainer, `{"features":{"ghcr.io/devcontainers/features/go:1":{}}}`, 1, "devcontainer feature"},
		{"feature no tag", CheckDevcontainer, `{"features":{"ghcr.io/devcontainers/features/go":{}}}`, 1, "exact version"},
		{"command", CheckDevcontainer, `{"postCreateCommand":["bash","-c","curl -s https://x | sh"]}`, 1, "piping a download"},
	})
}

func TestImageProblem(t *testing.T) {
	t.Parallel()
	tests := []struct {
		ref  string
		want string
	}{
		{pinned, ""},
		{"localhost:5000/x:1@" + digest, ""},
		{"localhost:5000/x@" + digest, "must carry a tag"},
		{"x:1", "tag and digest"},
		{"x:1@sha256:abc", "tag and digest"},
		{"docker://x:1@" + digest, ""},
	}
	for _, tt := range tests {
		t.Run(tt.ref, func(t *testing.T) {
			t.Parallel()
			got := imageProblem(tt.ref)
			if (tt.want == "") != (got == "") || !strings.Contains(got, tt.want) {
				t.Fatalf("imageProblem(%q) = %q, want %q", tt.ref, got, tt.want)
			}
		})
	}
}

func TestGitignore(t *testing.T) {
	t.Parallel()
	rules, err := parseGitignore("# c\n/bin/\n*.out\n/.claude/worktrees/\nbuild/\n!keep.out\ndocs/**/gen\n")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		path  string
		isDir bool
		want  bool
	}{
		{"bin", true, true},
		{"a/bin", true, false},
		{"x.out", false, true},
		{"a/b/x.out", false, true},
		{"keep.out", false, false},
		{".claude/worktrees", true, true},
		{".claude", true, false},
		{"a/build", true, true},
		{"a/build", false, false},
		{"docs/a/b/gen", true, true},
		{"docs/gen", true, true},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			t.Parallel()
			if got := rules.ignored(tt.path, tt.isDir); got != tt.want {
				t.Fatalf("ignored(%q, %v) = %v, want %v", tt.path, tt.isDir, got, tt.want)
			}
		})
	}
}

func TestScan(t *testing.T) {
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
	bad := "services:\n  a:\n    image: postgres:19.0\n"
	write(".gitignore", "/.claude/worktrees/\n*.local.yaml\n")
	write("go.mod", "module x\n\ngo 1.27.0\n\ntoolchain go1.27.4\n")
	write("docs/example/compose.yaml", bad)                                     // root docs: skipped
	write("deploy/compose/compose.yaml", bad)                                   // found
	write("deploy/docs/compose.yaml", bad)                                      // nested docs: found
	write("web/node_modules/x/package.json", `{"dependencies":{"a":"^1.0.0"}}`) // skipped anywhere
	write(".claude/worktrees/agent/compose.yaml", bad)                          // gitignored
	write("deploy/dev.local.yaml", bad)                                         // gitignored file
	write(".github/workflows/ci.yml", "jobs:\n  a:\n    steps:\n      - uses: actions/checkout@v7\n")
	write(".github/actions/setup/action.yml", "runs:\n  using: composite\n  steps:\n    - uses: actions/setup-go@v6\n")
	write("images/api.Dockerfile", "FROM golang:1.27.4\n")
	write("images/Containerfile", "FROM golang:1.27.4\n")
	write("infra/main.tofu", "terraform {\n  required_version = \"~> 1.10\"\n}\n")
	write(".devcontainer/devcontainer.json", `{"image":"golang:1.27.4"}`)
	write("go.work", "go 1.27.0\n")

	got, err := Scan(root)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	want := []string{
		filepath.Join(".devcontainer", "devcontainer.json"),
		filepath.Join(".github", "actions", "setup", "action.yml"),
		filepath.Join(".github", "workflows", "ci.yml"),
		filepath.Join("deploy", "compose", "compose.yaml"),
		filepath.Join("deploy", "docs", "compose.yaml"),
		"go.work",
		filepath.Join("images", "Containerfile"),
		filepath.Join("images", "api.Dockerfile"),
		filepath.Join("infra", "main.tofu"),
	}
	if len(got) != len(want) {
		t.Fatalf("got %d violations %v, want %d", len(got), got, len(want))
	}
	for i, v := range got {
		if v.File != want[i] {
			t.Errorf("violation %d in %s, want %s (%v)", i, v.File, want[i], got)
		}
	}
}
