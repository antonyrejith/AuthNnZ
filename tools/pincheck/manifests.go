package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var toolchainLine = regexp.MustCompile(`(?m)^toolchain go\d+\.\d+\.\d+\s*$`)

// CheckGoMod requires an exact toolchain directive.
func CheckGoMod(name string, data []byte) ([]Violation, error) {
	return checkToolchain(name, data, "go.mod"), nil
}

// CheckGoWork requires an exact toolchain directive in go.work.
func CheckGoWork(name string, data []byte) ([]Violation, error) {
	return checkToolchain(name, data, "go.work"), nil
}

func checkToolchain(name string, data []byte, kind string) []Violation {
	if !toolchainLine.Match(data) {
		return []Violation{{name, 1, kind + " must pin an exact toolchain (toolchain goX.Y.Z)"}}
	}
	return nil
}

var npmAlias = regexp.MustCompile(`^npm:(@[^@/]+/)?[^@]+@(.+)$`)

// npmVersionOK accepts an exact version, a workspace reference, or an npm:
// alias to an exact version. ref also accepts "$name" (an override that
// reuses a direct dependency's version, which is itself checked).
func npmVersionOK(v string, ref bool) bool {
	switch {
	case exactSemver.MatchString(v), strings.HasPrefix(v, "workspace:"):
		return true
	case ref && strings.HasPrefix(v, "$"):
		return true
	}
	if m := npmAlias.FindStringSubmatch(v); m != nil {
		return exactSemver.MatchString(m[2])
	}
	return false
}

// CheckPackageJSON requires exact versions in every dependency section, in
// overrides (npm), resolutions (Yarn) and pnpm.overrides, and in
// packageManager.
func CheckPackageJSON(name string, data []byte) ([]Violation, error) {
	var pkg map[string]json.RawMessage
	if err := json.Unmarshal(data, &pkg); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	var out []Violation
	bad := func(section, dep, v string) {
		out = append(out, Violation{name, lineOf(data, `"`+dep+`"`), fmt.Sprintf("%s %q: version %q is not an exact version", section, dep, v)})
	}
	for _, section := range []string{"dependencies", "devDependencies", "peerDependencies", "optionalDependencies", "resolutions"} {
		var deps map[string]string
		if err := decodeSection(pkg, section, &deps); err != nil {
			return nil, err
		}
		for _, dep := range sortedStrings(deps) {
			if !npmVersionOK(deps[dep], section == "resolutions") {
				bad(section, dep, deps[dep])
			}
		}
	}
	var overrides map[string]any
	if err := decodeSection(pkg, "overrides", &overrides); err != nil {
		return nil, err
	}
	walkOverrides("overrides", overrides, bad)
	var pnpm struct {
		Overrides map[string]string `json:"overrides"`
	}
	if err := decodeSection(pkg, "pnpm", &pnpm); err != nil {
		return nil, err
	}
	for _, dep := range sortedStrings(pnpm.Overrides) {
		if !npmVersionOK(pnpm.Overrides[dep], true) {
			bad("pnpm.overrides", dep, pnpm.Overrides[dep])
		}
	}
	var pm string
	if err := decodeSection(pkg, "packageManager", &pm); err != nil {
		return nil, err
	}
	if _, hasPM := pkg["packageManager"]; hasPM {
		if _, ver, found := strings.Cut(pm, "@"); !found || !exactSemver.MatchString(strings.SplitN(ver, "+", 2)[0]) {
			out = append(out, Violation{name, lineOf(data, `"packageManager"`), fmt.Sprintf("packageManager %q must name an exact version", pm)})
		}
	}
	return out, nil
}

func decodeSection(pkg map[string]json.RawMessage, key string, into any) error {
	raw, ok := pkg[key]
	if !ok {
		return nil
	}
	if err := json.Unmarshal(raw, into); err != nil {
		return fmt.Errorf("decode %s: %w", key, err)
	}
	return nil
}

// walkOverrides checks npm overrides, whose values are versions or nested
// objects (with "." naming the package's own version).
func walkOverrides(section string, m map[string]any, bad func(section, dep, v string)) {
	for _, dep := range sortedKeys(m) {
		switch v := m[dep].(type) {
		case string:
			if !npmVersionOK(v, true) {
				bad(section, dep, v)
			}
		case map[string]any:
			walkOverrides(section+"."+dep, v, bad)
		default:
			bad(section, dep, fmt.Sprint(v))
		}
	}
}

func sortedStrings(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// devcontainerCommands are the lifecycle hooks whose commands are checked.
var devcontainerCommands = []string{
	"initializeCommand", "onCreateCommand", "updateContentCommand",
	"postCreateCommand", "postStartCommand", "postAttachCommand",
}

// CheckDevcontainer checks devcontainer.json (JSON with comments): the image
// needs a tag and a digest, every feature an exact version or a digest, and
// lifecycle commands get the script checks.
func CheckDevcontainer(name string, data []byte) ([]Violation, error) {
	var dc map[string]any
	if err := json.Unmarshal(stripJSONC(data), &dc); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	var out []Violation
	if img, ok := dc["image"].(string); ok {
		out = append(out, imageViolation(name, lineOf(data, `"image"`), "devcontainer image", img)...)
	}
	features, _ := dc["features"].(map[string]any)
	for _, f := range sortedKeys(features) {
		if p := featureProblem(f); p != "" {
			out = append(out, Violation{name, lineOf(data, `"`+f+`"`), fmt.Sprintf("devcontainer feature %q %s", f, p)})
		}
	}
	for _, hook := range devcontainerCommands {
		line := lineOf(data, `"`+hook+`"`)
		for _, cmd := range commandStrings(dc[hook]) {
			out = append(out, commandViolations(name, line, cmd, nil)...)
		}
	}
	return out, nil
}

// featureProblem accepts local features, digests and exact X.Y.Z tags.
func featureProblem(ref string) string {
	if strings.HasPrefix(ref, "./") || strings.HasPrefix(ref, "../") {
		return ""
	}
	name, digest, hasDigest := strings.Cut(ref, "@")
	if hasDigest {
		if digestOnly.MatchString(digest) {
			return ""
		}
		return "has an invalid digest"
	}
	if tag := imageTag(name); exactSemver.MatchString(tag) {
		return ""
	}
	return "must be pinned to an exact version (X.Y.Z) or a digest"
}

func commandStrings(v any) []string {
	switch t := v.(type) {
	case string:
		return []string{t}
	case []any:
		parts := make([]string, 0, len(t))
		for _, p := range t {
			parts = append(parts, fmt.Sprint(p))
		}
		return []string{strings.Join(parts, " ")}
	case map[string]any:
		var out []string
		for _, k := range sortedKeys(t) {
			out = append(out, commandStrings(t[k])...)
		}
		return out
	default:
		return nil
	}
}

// stripJSONC removes // and /* */ comments and trailing commas outside
// strings, turning JSON with comments into JSON.
func stripJSONC(data []byte) []byte {
	out := make([]byte, 0, len(data))
	inStr := false
	for i := 0; i < len(data); i++ {
		c := data[i]
		switch {
		case inStr:
			out = append(out, c)
			if c == '\\' && i+1 < len(data) {
				i++
				out = append(out, data[i])
			} else if c == '"' {
				inStr = false
			}
		case c == '"':
			inStr = true
			out = append(out, c)
		case c == '/' && i+1 < len(data) && data[i+1] == '/':
			for i < len(data) && data[i] != '\n' {
				i++
			}
			i--
		case c == '/' && i+1 < len(data) && data[i+1] == '*':
			end := strings.Index(string(data[i+2:]), "*/")
			if end < 0 {
				return out
			}
			i += end + 3
		case c == ',' && nextSignificant(data, i+1) != 0 && strings.IndexByte("}]", nextSignificant(data, i+1)) >= 0:
			// trailing comma
		default:
			out = append(out, c)
		}
	}
	return out
}

func nextSignificant(data []byte, i int) byte {
	for ; i < len(data); i++ {
		switch data[i] {
		case ' ', '\t', '\r', '\n':
			continue
		case '/':
			if i+1 < len(data) && data[i+1] == '/' {
				for i < len(data) && data[i] != '\n' {
					i++
				}
				continue
			}
			return data[i]
		default:
			return data[i]
		}
	}
	return 0
}
