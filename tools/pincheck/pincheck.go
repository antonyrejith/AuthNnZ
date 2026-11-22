// Command pincheck enforces the version policy (decision D-15): every version
// is pinned exactly. It fails on version ranges, floating tags, container
// images without a digest, CI actions not pinned to a commit SHA, and @latest.
//
// Usage: pincheck [root]
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
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

// skipDirs are never scanned: documentation contains illustrative examples,
// and the others hold generated or third-party content.
var skipDirs = map[string]bool{
	".git": true, "bin": true, "dist": true, "docs": true,
	"node_modules": true, "testdata": true, "vendor": true,
}

var (
	digestRef     = regexp.MustCompile(`@sha256:[0-9a-f]{64}\b`)
	latestToken   = regexp.MustCompile(`(@latest\b|:latest\b)`)
	actionSHA     = regexp.MustCompile(`^[^@\s]+@[0-9a-f]{40}$`)
	exactSemver   = regexp.MustCompile(`^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)
	tfConstraint  = regexp.MustCompile(`^\s*(required_version|version)\s*=\s*"([^"]*)"`)
	toolchainLine = regexp.MustCompile(`(?m)^toolchain go\d+\.\d+\.\d+$`)
)

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

// Scan walks root and returns all violations, sorted by file and line.
func Scan(root string) ([]Violation, error) {
	var out []Violation
	//nolint:gosec // G703: walking the given root is this tool's purpose; it only reads files.
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return fmt.Errorf("relative path for %s: %w", path, relErr)
		}
		checker := checkerFor(rel)
		if checker == nil {
			return nil
		}
		data, readErr := os.ReadFile(path) //nolint:gosec // G304: reading files under the given root is this tool's purpose.
		if readErr != nil {
			return fmt.Errorf("read %s: %w", path, readErr)
		}
		vs, checkErr := checker(rel, bytes.NewReader(data))
		if checkErr != nil {
			return fmt.Errorf("check %s: %w", rel, checkErr)
		}
		out = append(out, vs...)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", root, err)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	return out, nil
}

type checker func(name string, r io.Reader) ([]Violation, error)

func checkerFor(rel string) checker {
	base := filepath.Base(rel)
	slashed := filepath.ToSlash(rel)
	switch {
	case base == "go.mod":
		return CheckGoMod
	case base == "package.json":
		return CheckPackageJSON
	case strings.HasPrefix(base, "Dockerfile") || strings.HasSuffix(base, ".dockerfile"):
		return CheckDockerfile
	case strings.HasPrefix(slashed, ".github/workflows/") && (strings.HasSuffix(base, ".yml") || strings.HasSuffix(base, ".yaml")):
		return CheckWorkflow
	case strings.HasSuffix(base, ".yml") || strings.HasSuffix(base, ".yaml"):
		return CheckYAML
	case strings.HasSuffix(base, ".tf"):
		return CheckTerraform
	case base == "Makefile" || strings.HasSuffix(base, ".sh") || strings.HasSuffix(base, ".mk"):
		return CheckScript
	default:
		return nil
	}
}

// eachLine calls fn for every line with its 1-based number.
func eachLine(r io.Reader, fn func(n int, line string)) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	n := 0
	for sc.Scan() {
		n++
		fn(n, sc.Text())
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("scan lines: %w", err)
	}
	return nil
}

func latestViolations(name string, n int, line string) []Violation {
	if strings.HasPrefix(strings.TrimSpace(line), "#") {
		return nil
	}
	if latestToken.MatchString(line) {
		return []Violation{{name, n, "floating version \"latest\" is not allowed"}}
	}
	return nil
}

// CheckGoMod requires an exact toolchain directive.
func CheckGoMod(name string, r io.Reader) ([]Violation, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}
	if !toolchainLine.Match(b) {
		return []Violation{{name, 1, "go.mod must pin an exact toolchain (toolchain goX.Y.Z)"}}, nil
	}
	return nil, nil
}

// CheckPackageJSON requires exact versions in every dependency section.
func CheckPackageJSON(name string, r io.Reader) ([]Violation, error) {
	var pkg map[string]json.RawMessage
	if err := json.NewDecoder(r).Decode(&pkg); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	var out []Violation
	for _, section := range []string{"dependencies", "devDependencies", "peerDependencies", "optionalDependencies"} {
		raw, ok := pkg[section]
		if !ok {
			continue
		}
		var deps map[string]string
		if err := json.Unmarshal(raw, &deps); err != nil {
			return nil, fmt.Errorf("decode %s: %w", section, err)
		}
		names := make([]string, 0, len(deps))
		for dep := range deps {
			names = append(names, dep)
		}
		sort.Strings(names)
		for _, dep := range names {
			v := deps[dep]
			if strings.HasPrefix(v, "workspace:") || exactSemver.MatchString(v) {
				continue
			}
			out = append(out, Violation{name, 1, fmt.Sprintf("%s %q: version %q is not an exact version", section, dep, v)})
		}
	}
	if pm, ok := pkg["packageManager"]; ok {
		var s string
		if err := json.Unmarshal(pm, &s); err != nil {
			return nil, fmt.Errorf("decode packageManager: %w", err)
		}
		if _, ver, found := strings.Cut(s, "@"); !found || !exactSemver.MatchString(strings.SplitN(ver, "+", 2)[0]) {
			out = append(out, Violation{name, 1, fmt.Sprintf("packageManager %q must name an exact version", s)})
		}
	}
	return out, nil
}

// CheckDockerfile requires digests on every external base image and on the
// syntax frontend.
func CheckDockerfile(name string, r io.Reader) ([]Violation, error) {
	var out []Violation
	stages := map[string]bool{}
	err := eachLine(r, func(n int, line string) {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)
		switch {
		case strings.HasPrefix(lower, "# syntax="):
			if !digestRef.MatchString(trimmed) {
				out = append(out, Violation{name, n, "Dockerfile syntax frontend must be pinned by digest"})
			}
		case strings.HasPrefix(lower, "from "):
			image, stage := parseFrom(trimmed)
			if stage != "" {
				stages[stage] = true
			}
			if image != "" && image != "scratch" && !stages[strings.ToLower(image)] && !digestRef.MatchString(image) {
				out = append(out, Violation{name, n, fmt.Sprintf("base image %q must be pinned by digest", image)})
			}
		default:
			out = append(out, latestViolations(name, n, line)...)
		}
	})
	return out, err
}

// parseFrom returns the image of a FROM instruction and the stage name it
// defines ("" when it defines none). Flags such as --platform are skipped.
func parseFrom(instruction string) (image, stage string) {
	fields := strings.Fields(instruction)
	for i := 1; i < len(fields); i++ {
		if strings.HasPrefix(fields[i], "--") {
			continue
		}
		if i+2 < len(fields) && strings.EqualFold(fields[i+1], "as") {
			return fields[i], strings.ToLower(fields[i+2])
		}
		return fields[i], ""
	}
	return "", ""
}

var yamlImage = regexp.MustCompile(`^\s*(-\s*)?image:\s*["']?([^"'\s#]+)`)

// CheckYAML requires digests on container images (Compose files, Kubernetes
// manifests, Helm values).
func CheckYAML(name string, r io.Reader) ([]Violation, error) {
	var out []Violation
	err := eachLine(r, func(n int, line string) {
		if m := yamlImage.FindStringSubmatch(line); m != nil {
			img := m[2]
			if strings.Contains(img, "{{") || strings.HasPrefix(img, "$") {
				return // templated; the values file is checked instead
			}
			if !digestRef.MatchString(img) {
				out = append(out, Violation{name, n, fmt.Sprintf("image %q must be pinned by digest", img)})
			}
			return
		}
		out = append(out, latestViolations(name, n, line)...)
	})
	return out, err
}

var usesLine = regexp.MustCompile(`^\s*(-\s*)?uses:\s*["']?([^"'\s#]+)`)

// CheckWorkflow requires CI actions pinned to full commit SHAs and container
// images pinned by digest.
func CheckWorkflow(name string, r io.Reader) ([]Violation, error) {
	var out []Violation
	err := eachLine(r, func(n int, line string) {
		if m := usesLine.FindStringSubmatch(line); m != nil {
			ref := m[2]
			switch {
			case strings.HasPrefix(ref, "./"):
			case strings.HasPrefix(ref, "docker://"):
				if !digestRef.MatchString(ref) {
					out = append(out, Violation{name, n, fmt.Sprintf("action image %q must be pinned by digest", ref)})
				}
			case !actionSHA.MatchString(ref):
				out = append(out, Violation{name, n, fmt.Sprintf("action %q must be pinned to a full commit SHA", ref)})
			}
			return
		}
		if m := yamlImage.FindStringSubmatch(line); m != nil && !digestRef.MatchString(m[2]) {
			out = append(out, Violation{name, n, fmt.Sprintf("image %q must be pinned by digest", m[2])})
			return
		}
		out = append(out, latestViolations(name, n, line)...)
	})
	return out, err
}

// CheckTerraform rejects version constraints that allow more than one version.
func CheckTerraform(name string, r io.Reader) ([]Violation, error) {
	var out []Violation
	err := eachLine(r, func(n int, line string) {
		m := tfConstraint.FindStringSubmatch(line)
		if m == nil {
			return
		}
		c := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(m[2]), "="))
		if !exactSemver.MatchString(c) {
			out = append(out, Violation{name, n, fmt.Sprintf("%s %q must be one exact version", m[1], m[2])})
		}
	})
	return out, err
}

// CheckScript rejects @latest and :latest in Makefiles and shell scripts.
func CheckScript(name string, r io.Reader) ([]Violation, error) {
	var out []Violation
	err := eachLine(r, func(n int, line string) {
		out = append(out, latestViolations(name, n, line)...)
	})
	return out, err
}
