// Command layercheck enforces the module layer rules of ADR-0016 on the
// packages that "go list" reports:
//
//   - a module's domain layer (internal/modules/*/internal/domain/...)
//     imports only the standard library, platform/ids and platform/errs;
//   - an application layer (.../internal/app/...) never imports an adapter
//     layer (.../internal/adapters/...), of any module;
//   - platform packages (internal/platform/...) never import a module.
//
// depguard (.golangci.yml) enforces the first and last rule while editing;
// it matches import paths by prefix only, so it cannot express the second,
// whose module name is a wildcard. This tool checks all three, including
// blank imports.
//
// Usage: layercheck [packages] (default ./...)
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
)

type pkg struct {
	ImportPath string
	Imports    []string
}

var (
	domainPkg   = regexp.MustCompile(`/internal/modules/[^/]+/internal/domain(/|$)`)
	appPkg      = regexp.MustCompile(`/internal/app(/|$)`)
	adapterPkg  = regexp.MustCompile(`/internal/adapters(/|$)`)
	platformPkg = regexp.MustCompile(`/internal/platform(/|$)`)
	modulePkg   = regexp.MustCompile(`/internal/modules(/|$)`)
	domainAllow = regexp.MustCompile(`/internal/platform/(ids|errs)$`)
)

func main() {
	patterns := os.Args[1:]
	if len(patterns) == 0 {
		patterns = []string{"./..."}
	}
	//nolint:gosec // G204: runs the go command on package patterns given by the developer.
	cmd := exec.CommandContext(context.Background(), "go", append([]string{"list", "-e", "-json=ImportPath,Imports"}, patterns...)...)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		fmt.Fprintln(os.Stderr, "layercheck: go list:", err)
		os.Exit(2)
	}
	pkgs, err := decode(bytes.NewReader(out))
	if err != nil {
		fmt.Fprintln(os.Stderr, "layercheck:", err)
		os.Exit(2)
	}
	problems := check(pkgs)
	for _, p := range problems {
		fmt.Println(p)
	}
	if len(problems) > 0 {
		fmt.Fprintf(os.Stderr, "layercheck: %d layer violation(s)\n", len(problems))
		os.Exit(1)
	}
}

func decode(r io.Reader) ([]pkg, error) {
	dec := json.NewDecoder(r)
	var pkgs []pkg
	for {
		var p pkg
		err := dec.Decode(&p)
		if errors.Is(err, io.EOF) {
			return pkgs, nil
		}
		if err != nil {
			return nil, fmt.Errorf("decode go list output: %w", err)
		}
		pkgs = append(pkgs, p)
	}
}

func check(pkgs []pkg) []string {
	var out []string
	for _, p := range pkgs {
		for _, imp := range p.Imports {
			if msg := violation(p.ImportPath, imp); msg != "" {
				out = append(out, fmt.Sprintf("%s imports %s: %s", p.ImportPath, imp, msg))
			}
		}
	}
	sort.Strings(out)
	return out
}

func violation(from, imp string) string {
	switch {
	case domainPkg.MatchString(from) && !isStdlib(imp) && !domainAllow.MatchString(imp):
		return "the domain layer may import only the standard library, platform/ids and platform/errs"
	case appPkg.MatchString(from) && adapterPkg.MatchString(imp):
		return "the application layer must not import an adapter layer"
	case platformPkg.MatchString(from) && modulePkg.MatchString(imp):
		return "platform packages must not import business modules"
	default:
		return ""
	}
}

// isStdlib reports standard-library import paths (no dot in the first path
// element).
func isStdlib(path string) bool {
	first, _, _ := strings.Cut(path, "/")
	return !strings.Contains(first, ".")
}
