// Command covercheck enforces per-package statement coverage (ADR-0017). It
// reads the output of "go test -cover ./..." on standard input, echoes it,
// and fails when a package is below its threshold, has no tests, or a test
// failed.
//
// Usage:
//
//	go test -cover ./... | covercheck -min 80 -strict-min 90 \
//	    -strict internal/platform/crypto -exclude internal/platform/db/dbtest
//
// Package patterns are module-relative and match the package and every
// package below it.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

func main() {
	minPct := flag.Float64("min", 80, "minimum coverage for every package (percent)")
	strictMin := flag.Float64("strict-min", 90, "minimum coverage for -strict packages (percent)")
	strict := flag.String("strict", "", "comma-separated module-relative packages held to -strict-min")
	exclude := flag.String("exclude", "", "comma-separated module-relative packages not measured")
	flag.Parse()
	cfg := config{min: *minPct, strictMin: *strictMin, strict: split(*strict), exclude: split(*exclude)}
	res, err := check(io.TeeReader(os.Stdin, os.Stdout), cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "covercheck:", err)
		os.Exit(2)
	}
	fmt.Print(res.report())
	if !res.ok() {
		os.Exit(1)
	}
}

type config struct {
	min, strictMin  float64
	strict, exclude []string
}

type pkgResult struct {
	path      string
	pct       float64
	threshold float64
	noTests   bool
}

type result struct {
	pkgs   []pkgResult
	failed []string
}

func (r result) ok() bool {
	if len(r.failed) > 0 {
		return false
	}
	for _, p := range r.pkgs {
		if p.noTests || p.pct < p.threshold {
			return false
		}
	}
	return true
}

func (r result) report() string {
	var b strings.Builder
	b.WriteString("\ncoverage gate:\n")
	for _, p := range r.pkgs {
		status := "ok"
		switch {
		case p.noTests:
			status = "FAIL (no tests)"
		case p.pct < p.threshold:
			status = "FAIL"
		}
		fmt.Fprintf(&b, "  %-60s %6.1f%%  (min %.0f%%)  %s\n", p.path, p.pct, p.threshold, status)
	}
	for _, f := range r.failed {
		fmt.Fprintf(&b, "  test failure: %s\n", f)
	}
	return b.String()
}

var (
	coverLine  = regexp.MustCompile(`^(?:ok\s+)?\s*(\S+)\s.*coverage: ([0-9.]+)% of statements`)
	noTestLine = regexp.MustCompile(`^\?\s+(\S+)\s+\[no test files\]`)
	failLine   = regexp.MustCompile(`^FAIL\s+(\S+)`)
)

func check(r io.Reader, cfg config) (result, error) {
	var res result
	seen := map[string]bool{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if m := failLine.FindStringSubmatch(line); m != nil {
			res.failed = append(res.failed, m[1])
			continue
		}
		var p pkgResult
		switch {
		case noTestLine.MatchString(line):
			p = pkgResult{path: noTestLine.FindStringSubmatch(line)[1], noTests: true}
		case coverLine.MatchString(line):
			m := coverLine.FindStringSubmatch(line)
			pct, err := strconv.ParseFloat(m[2], 64)
			if err != nil {
				return res, fmt.Errorf("parse coverage %q: %w", m[2], err)
			}
			p = pkgResult{path: m[1], pct: pct}
		default:
			continue
		}
		if seen[p.path] || matches(p.path, cfg.exclude) {
			continue
		}
		seen[p.path] = true
		p.threshold = cfg.min
		if matches(p.path, cfg.strict) {
			p.threshold = cfg.strictMin
		}
		res.pkgs = append(res.pkgs, p)
	}
	if err := sc.Err(); err != nil {
		return res, fmt.Errorf("read: %w", err)
	}
	sort.Slice(res.pkgs, func(i, j int) bool { return res.pkgs[i].path < res.pkgs[j].path })
	return res, nil
}

// matches reports whether the import path is one of the module-relative
// patterns or below one.
func matches(path string, patterns []string) bool {
	for _, p := range patterns {
		if strings.HasSuffix(path, "/"+p) || strings.Contains(path, "/"+p+"/") {
			return true
		}
	}
	return false
}

func split(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.Trim(strings.TrimSpace(p), "/"); p != "" {
			out = append(out, p)
		}
	}
	return out
}
