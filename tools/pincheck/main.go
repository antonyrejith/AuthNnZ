// Command pincheck enforces the version policy (decision D-15): every version
// is pinned exactly. It fails on version ranges, floating tags, container
// images without a tag and a digest, CI actions not pinned to a commit SHA,
// unpinned tool installs in scripts, and @latest.
//
// It understands Dockerfiles and Containerfiles, YAML (Compose, Kubernetes,
// Helm, Argo CD, GitHub workflows and actions), Terraform and OpenTofu
// (.tf, .tofu, .tf.json), Makefiles and shell scripts, package.json,
// devcontainer.json, go.mod and go.work.
//
// Usage: pincheck [root]
package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Violation is one place where a version is not pinned exactly.
type Violation struct {
	File    string
	Line    int
	Message string
}

func (v Violation) String() string {
	return fmt.Sprintf("%s:%d: %s", v.File, v.Line, v.Message)
}

// skipAnywhere are directories never scanned at any depth: version control
// metadata and third-party content.
var skipAnywhere = map[string]bool{".git": true, "node_modules": true, "vendor": true}

// skipAtRoot are top-level directories that are not scanned: documentation
// contains illustrative examples, the others hold build output and fixtures.
// A directory of the same name deeper in the tree is scanned.
var skipAtRoot = map[string]bool{"bin": true, "dist": true, "docs": true, "testdata": true}

func main() {
	root := "."
	if len(os.Args) > 1 {
		root = os.Args[1]
	}
	violations, err := Scan(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "pincheck:", err)
		os.Exit(2)
	}
	for _, v := range violations {
		fmt.Println(v)
	}
	if len(violations) > 0 {
		fmt.Fprintf(os.Stderr, "pincheck: %d unpinned version(s)\n", len(violations))
		os.Exit(1)
	}
}

// Scan walks root and returns all violations, sorted by file and line. Paths
// ignored by the root .gitignore are not scanned.
func Scan(root string) ([]Violation, error) {
	ignore, err := loadGitignore(root)
	if err != nil {
		return nil, err
	}
	var out []Violation
	//nolint:gosec // G703: walking the given root is this tool's purpose; it only reads files.
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		vs, visitErr := visit(root, path, d, ignore)
		out = append(out, vs...)
		return visitErr
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", root, err)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	return out, nil
}

// visit checks one entry below root. It returns filepath.SkipDir for
// directories that are not scanned.
func visit(root, path string, d fs.DirEntry, ignore ignoreRules) ([]Violation, error) {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return nil, fmt.Errorf("relative path for %s: %w", path, err)
	}
	slashed := filepath.ToSlash(rel)
	if d.IsDir() {
		atRoot := !strings.Contains(slashed, "/")
		if skipAnywhere[d.Name()] || (atRoot && skipAtRoot[d.Name()]) || ignore.ignored(slashed, true) {
			return nil, filepath.SkipDir
		}
		return nil, nil
	}
	if ignore.ignored(slashed, false) {
		return nil, nil
	}
	return checkFile(path, rel)
}

func checkFile(path, rel string) ([]Violation, error) {
	check := checkerFor(rel)
	if check == nil {
		return nil, nil
	}
	data, err := os.ReadFile(path) //nolint:gosec // G304: reading files under the given root is this tool's purpose.
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	vs, err := check(rel, data)
	if err != nil {
		return nil, fmt.Errorf("check %s: %w", rel, err)
	}
	return vs, nil
}

type checker func(name string, data []byte) ([]Violation, error)

func checkerFor(rel string) checker {
	base := filepath.Base(rel)
	lower := strings.ToLower(base)
	slashed := filepath.ToSlash(rel)
	isYAML := strings.HasSuffix(lower, ".yml") || strings.HasSuffix(lower, ".yaml")
	switch {
	case base == "go.mod":
		return CheckGoMod
	case base == "go.work":
		return CheckGoWork
	case base == "package.json":
		return CheckPackageJSON
	case base == "devcontainer.json" || base == ".devcontainer.json":
		return CheckDevcontainer
	case isDockerfile(base):
		return CheckDockerfile
	case isYAML && strings.HasPrefix(slashed, ".github/workflows/"):
		return CheckWorkflow
	case lower == "action.yml" || lower == "action.yaml":
		return CheckAction
	case lower == "chart.yaml" || lower == "chart.yml":
		return CheckChart
	case isYAML:
		return CheckYAML
	case strings.HasSuffix(lower, ".tf.json"):
		return CheckTerraformJSON
	case strings.HasSuffix(lower, ".tf") || strings.HasSuffix(lower, ".tofu"):
		return CheckTerraform
	case isScript(base):
		return CheckScript
	default:
		return nil
	}
}

func isDockerfile(base string) bool {
	lower := strings.ToLower(base)
	return lower == "dockerfile" || lower == "containerfile" ||
		strings.HasPrefix(lower, "dockerfile.") || strings.HasPrefix(lower, "containerfile.") ||
		strings.HasSuffix(lower, ".dockerfile") || strings.HasSuffix(lower, ".containerfile")
}

func isScript(base string) bool {
	switch base {
	case "Makefile", "makefile", "GNUmakefile":
		return true
	}
	for _, ext := range []string{".mk", ".sh", ".bash"} {
		if strings.HasSuffix(base, ext) {
			return true
		}
	}
	return false
}

// lines splits data into lines without their terminators.
func lines(data []byte) []string {
	s := strings.ReplaceAll(string(data), "\r\n", "\n")
	return strings.Split(s, "\n")
}

// lineOf returns the 1-based line of the first occurrence of needle, or 1.
func lineOf(data []byte, needle string) int {
	i := strings.Index(string(data), needle)
	if i < 0 {
		return 1
	}
	return strings.Count(string(data[:i]), "\n") + 1
}
