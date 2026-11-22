package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ignoreRule is one pattern of a .gitignore file.
type ignoreRule struct {
	re      *regexp.Regexp
	negate  bool
	dirOnly bool
}

// ignoreRules holds the root .gitignore. Git's rules apply in order and the
// last matching pattern wins. Nested .gitignore files are not read.
type ignoreRules []ignoreRule

func loadGitignore(root string) (ignoreRules, error) {
	data, err := os.ReadFile(filepath.Join(root, ".gitignore")) //nolint:gosec // G304: the root .gitignore of the scanned tree.
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read .gitignore: %w", err)
	}
	return parseGitignore(string(data))
}

func parseGitignore(src string) (ignoreRules, error) {
	var rules ignoreRules
	for _, raw := range strings.Split(src, "\n") {
		p := strings.TrimRight(raw, " \t\r")
		if p == "" || strings.HasPrefix(p, "#") {
			continue
		}
		var r ignoreRule
		if strings.HasPrefix(p, "!") {
			r.negate, p = true, p[1:]
		} else if strings.HasPrefix(p, `\!`) || strings.HasPrefix(p, `\#`) {
			p = p[1:]
		}
		if strings.HasSuffix(p, "/") {
			r.dirOnly, p = true, strings.TrimRight(p, "/")
		}
		anchored := strings.Contains(p, "/")
		p = strings.TrimPrefix(p, "/")
		if p == "" {
			continue
		}
		expr := globToRegexp(p)
		if anchored {
			expr = "^" + expr + "$"
		} else {
			expr = "^(?:.*/)?" + expr + "$"
		}
		re, err := regexp.Compile(expr)
		if err != nil {
			return nil, fmt.Errorf(".gitignore pattern %q: %w", raw, err)
		}
		r.re = re
		rules = append(rules, r)
	}
	return rules, nil
}

// ignored reports whether the slash-separated path rel is ignored.
func (rules ignoreRules) ignored(rel string, isDir bool) bool {
	out := false
	for _, r := range rules {
		if r.dirOnly && !isDir {
			continue
		}
		if r.re.MatchString(rel) {
			out = !r.negate
		}
	}
	return out
}

// globToRegexp converts a gitignore glob (with **, *, ? and classes) into a
// regular expression body.
func globToRegexp(glob string) string {
	var b strings.Builder
	for i := 0; i < len(glob); i++ {
		c := glob[i]
		switch {
		case strings.HasPrefix(glob[i:], "**/"):
			b.WriteString("(?:.*/)?")
			i += 2
		case strings.HasPrefix(glob[i:], "**"):
			b.WriteString(".*")
			i++
		case c == '*':
			b.WriteString("[^/]*")
		case c == '?':
			b.WriteString("[^/]")
		case c == '[':
			end := strings.IndexByte(glob[i+1:], ']')
			if end < 0 {
				b.WriteString(`\[`)
				continue
			}
			class := glob[i+1 : i+1+end]
			if strings.HasPrefix(class, "!") {
				class = "^" + class[1:]
			}
			b.WriteString("[" + strings.ReplaceAll(class, `\`, `\\`) + "]")
			i += end + 1
		case c == '\\' && i+1 < len(glob):
			i++
			b.WriteString(regexp.QuoteMeta(string(glob[i])))
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	return b.String()
}
