package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

type yamlKind int

const (
	yamlPlain    yamlKind = iota // Compose, Kubernetes, Helm values, Argo CD, …
	yamlWorkflow                 // .github/workflows/*
	yamlAction                   // action.yml / action.yaml (composite, docker)
	yamlChart                    // Helm Chart.yaml
)

// CheckYAML checks container images (scalar "image" values and
// repository/tag/digest maps), Compose build.dockerfile_inline, Argo CD
// targetRevision, Kustomize images, "run" scripts, and "latest" anywhere.
func CheckYAML(name string, data []byte) ([]Violation, error) {
	return checkYAML(name, data, yamlPlain)
}

// CheckWorkflow additionally requires actions pinned to full commit SHAs,
// job container and service images pinned, and no "*-latest" runner labels.
func CheckWorkflow(name string, data []byte) ([]Violation, error) {
	return checkYAML(name, data, yamlWorkflow)
}

// CheckAction applies the workflow rules to a composite or Docker action.
func CheckAction(name string, data []byte) ([]Violation, error) {
	return checkYAML(name, data, yamlAction)
}

// CheckChart additionally requires exact versions on chart dependencies.
func CheckChart(name string, data []byte) ([]Violation, error) {
	return checkYAML(name, data, yamlChart)
}

func checkYAML(name string, data []byte, kind yamlKind) ([]Violation, error) {
	w := &yamlWalker{name: name, kind: kind}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	for {
		var doc yaml.Node
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if bytes.Contains(data, []byte("{{")) {
				// A Go template (Helm chart template) is not YAML until
				// rendered; check it line by line instead.
				return templateLines(name, data), nil
			}
			return nil, fmt.Errorf("parse YAML: %w", err)
		}
		w.walk(&doc, nil)
	}
	return w.out, nil
}

var templateImage = regexp.MustCompile(`^\s*(-\s*)?image:\s*["']?([^"'\s#]+)`)

func templateLines(name string, data []byte) []Violation {
	var out []Violation
	for i, l := range lines(data) {
		if m := templateImage.FindStringSubmatch(l); m != nil {
			if !strings.Contains(m[2], "{{") {
				out = append(out, imageViolation(name, i+1, "image", m[2])...)
			}
			continue
		}
		out = append(out, latestViolations(name, i+1, l)...)
	}
	return out
}

type yamlWalker struct {
	name string
	kind yamlKind
	out  []Violation
}

func (w *yamlWalker) add(vs ...Violation) { w.out = append(w.out, vs...) }

// walk visits n; path holds the mapping keys from the document root.
func (w *yamlWalker) walk(n *yaml.Node, path []string) {
	switch n.Kind {
	case yaml.DocumentNode, yaml.SequenceNode:
		for _, c := range n.Content {
			w.walk(c, path)
		}
	case yaml.MappingNode:
		w.mapping(n, path)
	case yaml.ScalarNode:
		w.add(latestViolations(w.name, n.Line, n.Value)...)
	case yaml.AliasNode:
		// The anchor is checked where it is defined.
	}
}

func (w *yamlWalker) mapping(n *yaml.Node, path []string) {
	if repo := field(n, "repository"); repo != nil && (field(n, "tag") != nil || field(n, "digest") != nil) && !isScalarKey(path, "image") {
		w.imageMap(n, repo)
	}
	if last(path) == "images" && (field(n, "newTag") != nil || field(n, "newName") != nil) {
		w.kustomizeImage(n)
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		child := append(append([]string(nil), path...), k.Value)
		if w.rule(k.Value, v, path) {
			continue
		}
		w.walk(v, child)
	}
}

// rule applies the key-specific checks. It reports whether v was fully
// handled (and must not be walked again).
func (w *yamlWalker) rule(key string, v *yaml.Node, path []string) bool {
	scalar := v.Kind == yaml.ScalarNode
	ci := w.kind == yamlWorkflow || w.kind == yamlAction
	switch {
	case key == "image" && scalar:
		w.imageScalar(v, path)
		return true
	case key == "image" && v.Kind == yaml.MappingNode:
		repo := field(v, "repository")
		if repo == nil {
			repo = field(v, "name")
		}
		w.imageMap(v, repo)
		return false
	case key == "uses" && scalar && ci:
		w.uses(v)
		return true
	case key == "container" && scalar && w.kind == yamlWorkflow:
		w.add(imageViolation(w.name, v.Line, "job container image", v.Value)...)
		return true
	case key == "runs-on" && w.kind == yamlWorkflow:
		w.runsOn(v)
		return true
	case key == "targetRevision" && scalar:
		w.targetRevision(v)
		return true
	case key == "dockerfile_inline" && scalar:
		w.add(checkDockerfileAt(w.name, contentLine(v)-1, v.Value)...)
		return true
	case key == "run" && scalar:
		w.script(v)
		return true
	case key == "dependencies" && w.kind == yamlChart && len(path) == 0 && v.Kind == yaml.SequenceNode:
		w.chartDependencies(v)
		return false
	}
	return false
}

func (w *yamlWalker) imageScalar(v *yaml.Node, path []string) {
	ref := v.Value
	if strings.Contains(ref, "{{") {
		return // templated; the values file is checked instead
	}
	if w.kind == yamlAction && last(path) == "runs" && !strings.HasPrefix(ref, "docker://") {
		return // a Dockerfile next to the action; the Dockerfile is checked
	}
	w.add(imageViolation(w.name, v.Line, "image", ref)...)
}

// imageMap checks a {repository|name, tag, digest} image description.
func (w *yamlWalker) imageMap(n, repo *yaml.Node) {
	line, label := n.Line, "image"
	if repo != nil {
		line, label = repo.Line, fmt.Sprintf("image %q", repo.Value)
	}
	tag, digest := scalarField(n, "tag"), scalarField(n, "digest")
	if t, d, ok := strings.Cut(tag, "@"); ok && digest == "" {
		tag, digest = t, d
	}
	switch {
	case strings.Contains(tag+digest, "{{"):
	case tag == "" || tag == "latest":
		w.add(Violation{w.name, line, label + " must set an exact tag (not empty or \"latest\")"})
	case !digestOnly.MatchString(digest):
		w.add(Violation{w.name, line, label + " must set a digest (sha256:…) as well as a tag"})
	}
}

func (w *yamlWalker) kustomizeImage(n *yaml.Node) {
	if !digestOnly.MatchString(scalarField(n, "digest")) {
		w.add(Violation{w.name, n.Line, fmt.Sprintf("kustomize image %q must set a digest", scalarField(n, "name"))})
	}
}

func (w *yamlWalker) uses(v *yaml.Node) {
	ref := v.Value
	switch {
	case strings.HasPrefix(ref, "./"):
	case strings.HasPrefix(ref, "docker://"):
		w.add(imageViolation(w.name, v.Line, "action image", ref)...)
	case !actionSHA.MatchString(ref):
		w.add(Violation{w.name, v.Line, fmt.Sprintf("action %q must be pinned to a full commit SHA", ref)})
	}
}

func (w *yamlWalker) runsOn(v *yaml.Node) {
	switch v.Kind {
	case yaml.ScalarNode:
		if strings.HasSuffix(v.Value, "-latest") {
			w.add(Violation{w.name, v.Line, fmt.Sprintf("runner %q is a floating image; name an exact runner version", v.Value)})
		}
		w.add(latestViolations(w.name, v.Line, v.Value)...)
	case yaml.SequenceNode, yaml.MappingNode, yaml.DocumentNode:
		for _, c := range v.Content {
			w.runsOn(c)
		}
	case yaml.AliasNode:
	}
}

// targetRevision (Argo CD) must be a commit SHA or an exact version tag.
func (w *yamlWalker) targetRevision(v *yaml.Node) {
	r := v.Value
	if commitSHA.MatchString(r) || exactSemver.MatchString(strings.TrimPrefix(r, "v")) {
		return
	}
	w.add(Violation{w.name, v.Line, fmt.Sprintf("targetRevision %q must be a commit SHA or an exact version tag", r)})
}

func (w *yamlWalker) script(v *yaml.Node) {
	first := contentLine(v)
	for _, ll := range joinContinuations(lines([]byte(v.Value)), '\\') {
		w.add(commandViolations(w.name, first+ll.line-1, ll.text, nil)...)
	}
}

func (w *yamlWalker) chartDependencies(v *yaml.Node) {
	for _, dep := range v.Content {
		if dep.Kind != yaml.MappingNode {
			continue
		}
		ver := field(dep, "version")
		if ver == nil || !exactVersion(ver.Value) || strings.HasPrefix(strings.TrimSpace(ver.Value), "=") {
			line := dep.Line
			if ver != nil {
				line = ver.Line
			}
			w.add(Violation{w.name, line, fmt.Sprintf("chart dependency %q must have an exact version", scalarField(dep, "name"))})
		}
	}
}

// contentLine is the line where a scalar's text starts (block scalars start
// on the line after their indicator).
func contentLine(v *yaml.Node) int {
	if v.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
		return v.Line + 1
	}
	return v.Line
}

func field(n *yaml.Node, key string) *yaml.Node {
	if n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

func scalarField(n *yaml.Node, key string) string {
	if v := field(n, key); v != nil && v.Kind == yaml.ScalarNode {
		return v.Value
	}
	return ""
}

func last(path []string) string {
	if len(path) == 0 {
		return ""
	}
	return path[len(path)-1]
}

func isScalarKey(path []string, key string) bool { return last(path) == key }
