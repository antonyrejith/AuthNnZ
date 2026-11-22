package main

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	assignLine = regexp.MustCompile(`^\s*(?:export\s+|override\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*(?:::=|:=|\?=|\+=|=)\s*(.*)$`)
	varRef     = regexp.MustCompile(`\$\(([A-Za-z_][A-Za-z0-9_]*)\)|\$\{([A-Za-z_][A-Za-z0-9_]*)(?::?-([^}]*))?\}|\$([A-Za-z_][A-Za-z0-9_]*)`)
	pipeToSh   = regexp.MustCompile(`\b(curl|wget)\b[^|;&]*\|\s*(sudo\s+(-\S+\s+)*)?(ba|z|da|k)?sh\b`)
	goInstall  = regexp.MustCompile(`\bgo\s+(?:-C\s+\S+\s+)?(install|run)\b([^;&|]*)`)
	goModVer   = regexp.MustCompile(`^['"]?([A-Za-z0-9][A-Za-z0-9.\-_~]*\.[A-Za-z]+/[^@\s'"]*)@([^\s'"]+)['"]?$`)
	npmGlobal  = regexp.MustCompile(`\b(?:npm\s+(?:install|i|add)|pnpm\s+(?:add|install|i)|yarn\s+global\s+add)\b([^;&|]*)`)
	dockerRun  = regexp.MustCompile(`\b(?:docker|podman)\s+(?:container\s+)?run\b([^;&|]*)`)
)

// dockerRunBoolFlags take no value; every other flag without "=" takes the
// next argument as its value.
var dockerRunBoolFlags = map[string]bool{
	"--rm": true, "--init": true, "--privileged": true, "--read-only": true,
	"--detach": true, "--interactive": true, "--tty": true, "--publish-all": true,
	"--no-healthcheck": true, "--oom-kill-disable": true, "--sig-proxy": true,
	"--quiet": true, "-q": true,
}

// CheckScript checks Makefiles and shell scripts: @latest and :latest,
// "go install|run pkg@ver" without an exact vX.Y.Z, "docker run" without a
// tag and digest, global npm installs without an exact version, and
// downloads piped into a shell. Variables assigned in the same file are
// resolved.
func CheckScript(name string, data []byte) ([]Violation, error) {
	src := lines(data)
	vars := map[string]string{}
	for _, l := range src {
		if m := assignLine.FindStringSubmatch(l); m != nil && !strings.HasPrefix(l, "\t") {
			vars[m[1]] = strings.Trim(strings.TrimSpace(m[2]), `"'`)
		}
	}
	var out []Violation
	for _, ll := range joinContinuations(src, '\\') {
		out = append(out, commandViolations(name, ll.line, ll.text, vars)...)
	}
	return out, nil
}

type logicalLine struct {
	line int
	text string
}

// joinContinuations joins lines ending in the escape character, skipping
// comment lines, and reports each logical line at its first physical line.
func joinContinuations(src []string, escape byte) []logicalLine {
	var out []logicalLine
	var cur strings.Builder
	start := 0
	for i, l := range src {
		trimmed := strings.TrimSpace(l)
		if cur.Len() == 0 {
			start = i + 1
			if strings.HasPrefix(trimmed, "#") {
				out = append(out, logicalLine{start, l}) // a comment never continues
				continue
			}
		} else if strings.HasPrefix(trimmed, "#") || trimmed == "" {
			continue // comments and blank lines inside a continuation
		}
		if strings.HasSuffix(strings.TrimRight(l, " \t"), string(escape)) {
			r := strings.TrimRight(l, " \t")
			cur.WriteString(r[:len(r)-1] + " ")
			continue
		}
		cur.WriteString(l)
		out = append(out, logicalLine{start, cur.String()})
		cur.Reset()
	}
	if cur.Len() > 0 {
		out = append(out, logicalLine{start, cur.String()})
	}
	return out
}

// commandViolations checks one logical shell command line.
func commandViolations(file string, line int, text string, vars map[string]string) []Violation {
	if strings.HasPrefix(strings.TrimSpace(text), "#") {
		return nil
	}
	var out []Violation
	out = append(out, latestViolations(file, line, text)...)
	if pipeToSh.MatchString(text) {
		out = append(out, Violation{file, line, "piping a download into a shell is an unpinned install; download a pinned version and verify its checksum"})
	}
	for _, m := range goInstall.FindAllStringSubmatch(text, -1) {
		out = append(out, goInstallViolations(file, line, m[1], m[2])...)
	}
	for _, m := range npmGlobal.FindAllStringSubmatch(text, -1) {
		out = append(out, npmGlobalViolations(file, line, m[0], m[1])...)
	}
	for _, m := range dockerRun.FindAllStringSubmatch(text, -1) {
		out = append(out, dockerRunViolations(file, line, m[1], vars)...)
	}
	return out
}

func goInstallViolations(file string, line int, verb, args string) []Violation {
	var out []Violation
	for _, f := range strings.Fields(args) {
		m := goModVer.FindStringSubmatch(f)
		if m == nil {
			continue
		}
		if !goSemver.MatchString(m[2]) && m[2] != "latest" { // latest is reported on its own
			out = append(out, Violation{file, line, fmt.Sprintf("go %s %s@%s must name an exact version vX.Y.Z", verb, m[1], m[2])})
		}
		if verb == "run" {
			break // later arguments belong to the program
		}
	}
	return out
}

func npmGlobalViolations(file string, line int, whole, args string) []Violation {
	fields := strings.Fields(args)
	global := strings.Contains(whole, "yarn")
	for _, f := range fields {
		if f == "-g" || f == "--global" || f == "--location=global" {
			global = true
		}
	}
	if !global {
		return nil // project installs are governed by package.json
	}
	var out []Violation
	for _, f := range fields {
		if strings.HasPrefix(f, "-") {
			continue
		}
		pkg := strings.Trim(f, `"'`)
		at := strings.LastIndexByte(pkg, '@')
		if at <= 0 || !exactSemver.MatchString(pkg[at+1:]) {
			out = append(out, Violation{file, line, fmt.Sprintf("global npm install of %q must name an exact version (name@X.Y.Z)", pkg)})
		}
	}
	return out
}

func dockerRunViolations(file string, line int, args string, vars map[string]string) []Violation {
	fields := strings.Fields(args)
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		if strings.HasPrefix(f, "-") {
			if !strings.Contains(f, "=") && !dockerRunBoolFlags[f] && !isShortBoolCluster(f) {
				i++ // the flag's value
			}
			continue
		}
		raw := strings.Trim(f, `"'`)
		ref, ok := expandVars(raw, vars)
		if !ok {
			return []Violation{{file, line, fmt.Sprintf("docker run image %q uses a variable that is not defined in this file; use a literal name:tag@sha256:… reference", raw)}}
		}
		return imageViolation(file, line, "docker run image", ref)
	}
	return nil
}

// isShortBoolCluster reports short boolean flags such as -it, -d or -dit.
func isShortBoolCluster(f string) bool {
	if len(f) < 2 || f[0] != '-' || f[1] == '-' {
		return false
	}
	for _, c := range f[1:] {
		if !strings.ContainsRune("itdP", c) {
			return false
		}
	}
	return true
}

// expandVars replaces $(X), ${X}, ${X:-default} and $X using vars. It fails
// when a variable is unknown and has no default.
func expandVars(s string, vars map[string]string) (string, bool) {
	ok := true
	out := varRef.ReplaceAllStringFunc(s, func(ref string) string {
		m := varRef.FindStringSubmatch(ref)
		name := m[1] + m[2] + m[4]
		if v, found := vars[name]; found && v != "" {
			return v
		}
		if strings.Contains(ref, "-") && m[2] != "" {
			return m[3]
		}
		ok = false
		return ref
	})
	return out, ok
}
