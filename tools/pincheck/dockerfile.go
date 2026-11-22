package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var (
	syntaxDirective = regexp.MustCompile(`(?i)^#\s*syntax\s*=\s*(\S+)`)
	escapeDirective = regexp.MustCompile("(?i)^#\\s*escape\\s*=\\s*([\\\\`])")
)

// dockerfile holds what the checker learns while reading a Dockerfile.
type dockerfile struct {
	name   string
	offset int               // added to line numbers (inline Dockerfiles)
	args   map[string]string // ARG defaults; "" means declared without one
	stages map[string]bool   // stage names and indexes, lower case
	nStage int
	out    []Violation
}

// CheckDockerfile requires a tag and a digest on the syntax frontend, on every
// external base image (FROM, with build arguments resolved), and on images
// named by COPY --from and RUN --mount=from=. RUN commands get the script
// checks.
func CheckDockerfile(name string, data []byte) ([]Violation, error) {
	return checkDockerfileAt(name, 0, string(data)), nil
}

func checkDockerfileAt(name string, offset int, src string) []Violation {
	d := &dockerfile{name: name, offset: offset, args: map[string]string{}, stages: map[string]bool{}}
	raw := lines([]byte(src))
	escape := byte('\\')
	for _, l := range raw {
		t := strings.TrimSpace(l)
		if !strings.HasPrefix(t, "#") {
			break
		}
		if m := escapeDirective.FindStringSubmatch(t); m != nil {
			escape = m[1][0]
		}
	}
	for i, l := range raw {
		if m := syntaxDirective.FindStringSubmatch(strings.TrimSpace(l)); m != nil {
			d.out = append(d.out, imageViolation(name, offset+i+1, "Dockerfile syntax frontend", m[1])...)
		}
	}
	for _, ins := range joinContinuations(raw, escape) {
		t := strings.TrimSpace(ins.text)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		d.instruction(offset+ins.line, t)
	}
	return d.out
}

func (d *dockerfile) instruction(line int, text string) {
	fields := strings.Fields(text)
	switch strings.ToUpper(fields[0]) {
	case "ARG":
		d.arg(fields[1:])
	case "FROM":
		d.from(line, fields[1:])
	case "COPY", "ADD":
		for _, f := range fields[1:] {
			if v, ok := strings.CutPrefix(f, "--from="); ok {
				d.fromRef(line, "COPY --from image", v)
			}
		}
		d.out = append(d.out, latestViolations(d.name, line, text)...)
	case "RUN":
		d.run(line, fields[1:])
	default:
		d.out = append(d.out, latestViolations(d.name, line, text)...)
	}
}

func (d *dockerfile) arg(fields []string) {
	for _, f := range fields {
		k, v, hasDefault := strings.Cut(f, "=")
		if !hasDefault {
			if _, known := d.args[k]; !known {
				d.args[k] = ""
			}
			continue
		}
		d.args[k] = strings.Trim(v, `"'`)
	}
}

func (d *dockerfile) from(line int, fields []string) {
	var image string
	rest := fields
	for len(rest) > 0 && strings.HasPrefix(rest[0], "--") {
		rest = rest[1:]
	}
	if len(rest) > 0 {
		image = rest[0]
	}
	if image != "" {
		d.fromRef(line, "base image", image)
	}
	d.stages[strconv.Itoa(d.nStage)] = true
	d.nStage++
	if len(rest) >= 3 && strings.EqualFold(rest[1], "as") {
		d.stages[strings.ToLower(rest[2])] = true
	}
}

// fromRef checks an image reference that may also name an earlier stage.
func (d *dockerfile) fromRef(line int, what, raw string) {
	ref, ok := expandVars(raw, d.args)
	if !ok {
		d.out = append(d.out, Violation{d.name, line, fmt.Sprintf("%s %q uses a build argument without a pinned default", what, raw)})
		return
	}
	if strings.EqualFold(ref, "scratch") || d.stages[strings.ToLower(ref)] {
		return
	}
	if ref != raw {
		what = fmt.Sprintf("%s %q (resolved)", what, raw)
		if p := imageProblem(ref); p != "" {
			d.out = append(d.out, Violation{d.name, line, fmt.Sprintf("%s to %q %s", what, ref, p)})
		}
		return
	}
	d.out = append(d.out, imageViolation(d.name, line, what, ref)...)
}

func (d *dockerfile) run(line int, fields []string) {
	i := 0
	for ; i < len(fields) && strings.HasPrefix(fields[i], "--"); i++ {
		spec, ok := strings.CutPrefix(fields[i], "--mount=")
		if !ok {
			continue
		}
		for _, kv := range strings.Split(spec, ",") {
			if v, isFrom := strings.CutPrefix(kv, "from="); isFrom {
				d.fromRef(line, "RUN --mount from image", v)
			}
		}
	}
	d.out = append(d.out, commandViolations(d.name, line, strings.Join(fields[i:], " "), d.args)...)
}
