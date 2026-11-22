package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// CheckTerraform checks Terraform and OpenTofu configuration (.tf, .tofu):
// terraform.required_version and every required_providers entry (string or
// object form) need one exact version; registry modules need an exact
// version; git module sources must pin ref= to a full commit SHA; provider
// blocks that set a version must set an exact one.
func CheckTerraform(name string, data []byte) ([]Violation, error) {
	body := parseHCL(lexHCL(string(data)))
	tf := &tfChecker{name: name}
	for _, b := range body.blocks {
		switch b.typ {
		case "terraform":
			tf.terraformBlock(b)
		case "module":
			src, hasSrc := b.attr("source")
			ver, hasVer := b.attr("version")
			tf.module(b.line, label(b), src, hasSrc, ver, hasVer)
		case "provider":
			if v, ok := b.attr("version"); ok {
				tf.exact(v, fmt.Sprintf("provider %q version", label(b)))
			}
		}
	}
	return tf.out, nil
}

type tfChecker struct {
	name string
	out  []Violation
}

func (c *tfChecker) add(line int, msg string) { c.out = append(c.out, Violation{c.name, line, msg}) }

func (c *tfChecker) exact(v hclValue, what string) {
	if v.str == nil {
		c.add(v.line, what+" must be a literal exact version")
		return
	}
	if !exactVersion(*v.str) || strings.HasPrefix(strings.TrimSpace(*v.str), "v") {
		c.add(v.line, fmt.Sprintf("%s %q must be one exact version", what, *v.str))
	}
}

func (c *tfChecker) terraformBlock(b *hclBlock) {
	if v, ok := b.attr("required_version"); ok {
		c.exact(v, "required_version")
	}
	for _, rp := range b.blocks {
		if rp.typ != "required_providers" {
			continue
		}
		for _, e := range rp.attrs {
			what := fmt.Sprintf("required_providers %q version", e.name)
			switch {
			case e.val.obj != nil:
				v, ok := e.val.field("version")
				if !ok {
					c.add(e.val.line, what+" is missing; set one exact version")
					continue
				}
				c.exact(v, what)
			default:
				c.exact(e.val, what)
			}
		}
	}
}

func (c *tfChecker) module(line int, name string, src hclValue, hasSrc bool, version hclValue, hasVersion bool) {
	if !hasSrc || src.str == nil {
		c.add(line, fmt.Sprintf("module %q must have a literal source", name))
		return
	}
	s := *src.str
	switch moduleSourceKind(s) {
	case srcLocal:
	case srcGit:
		if !gitRefPinned(s) {
			c.add(src.line, fmt.Sprintf("module %q git source %q must pin ref= to a full commit SHA", name, s))
		}
	case srcRegistry:
		if !hasVersion {
			c.add(line, fmt.Sprintf("registry module %q must set one exact version", name))
			return
		}
		c.exact(version, fmt.Sprintf("module %q version", name))
	case srcOther:
		c.add(src.line, fmt.Sprintf("module %q source %q cannot be pinned; use a registry module with an exact version or a git source pinned to a commit SHA", name, s))
	}
}

type sourceKind int

const (
	srcLocal sourceKind = iota
	srcGit
	srcRegistry
	srcOther
)

func moduleSourceKind(s string) sourceKind {
	switch {
	case strings.HasPrefix(s, "./") || strings.HasPrefix(s, "../"):
		return srcLocal
	case strings.HasPrefix(s, "git::") || strings.HasPrefix(s, "git@") ||
		strings.HasPrefix(s, "github.com/") || strings.HasPrefix(s, "bitbucket.org/"):
		return srcGit
	case strings.Contains(s, "::") || strings.Contains(s, "://") || strings.HasPrefix(s, "/"):
		return srcOther
	default:
		addr, _, _ := strings.Cut(s, "//")
		if n := strings.Count(addr, "/"); n == 2 || n == 3 {
			return srcRegistry
		}
		return srcOther
	}
}

func gitRefPinned(s string) bool {
	_, query, ok := strings.Cut(s, "?")
	if !ok {
		return false
	}
	q, err := url.ParseQuery(query)
	return err == nil && commitSHA.MatchString(q.Get("ref"))
}

func label(b *hclBlock) string {
	if len(b.labels) == 0 {
		return ""
	}
	return b.labels[0]
}

// CheckTerraformJSON applies the CheckTerraform rules to .tf.json files.
func CheckTerraformJSON(name string, data []byte) ([]Violation, error) {
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	c := &tfChecker{name: name}
	jv := func(v any, key string) hclValue {
		line := lineOf(data, `"`+key+`"`)
		if s, ok := v.(string); ok {
			return hclValue{line: line, str: &s}
		}
		return hclValue{line: line}
	}
	c.jsonTerraform(doc, data, jv)
	for _, mods := range objects(doc["module"]) {
		for _, m := range sortedKeys(mods) {
			for _, mod := range objects(mods[m]) {
				src, hasSrc := mod["source"]
				ver, hasVer := mod["version"]
				c.module(lineOf(data, `"`+m+`"`), m, jv(src, m), hasSrc, jv(ver, m), hasVer)
			}
		}
	}
	return c.out, nil
}

// jsonTerraform checks the terraform blocks of a .tf.json document.
func (c *tfChecker) jsonTerraform(doc map[string]any, data []byte, jv func(v any, key string) hclValue) {
	for _, tf := range objects(doc["terraform"]) {
		if v, ok := tf["required_version"]; ok {
			c.exact(jv(v, "required_version"), "required_version")
		}
		for _, rp := range objects(tf["required_providers"]) {
			for _, p := range sortedKeys(rp) {
				what := fmt.Sprintf("required_providers %q version", p)
				if obj, ok := rp[p].(map[string]any); ok {
					v, has := obj["version"]
					if !has {
						c.add(lineOf(data, `"`+p+`"`), what+" is missing; set one exact version")
						continue
					}
					c.exact(jv(v, p), what)
					continue
				}
				c.exact(jv(rp[p], p), what)
			}
		}
	}
}

// objects returns v as a list of JSON objects (Terraform JSON allows a block
// to be an object or an array of objects).
func objects(v any) []map[string]any {
	switch t := v.(type) {
	case map[string]any:
		return []map[string]any{t}
	case []any:
		var out []map[string]any
		for _, e := range t {
			if m, ok := e.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	default:
		return nil
	}
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
