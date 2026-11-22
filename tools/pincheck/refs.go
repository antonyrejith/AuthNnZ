package main

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	digestOnly  = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	latestToken = regexp.MustCompile(`(@latest\b|:latest\b)`)
	actionSHA   = regexp.MustCompile(`^[^@\s]+@[0-9a-f]{40}$`)
	commitSHA   = regexp.MustCompile(`^[0-9a-f]{40}$`)
	// exactSemver is one exact semantic version, without a "v" prefix.
	exactSemver = regexp.MustCompile(`^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)
	// goSemver is one exact Go module version ("v" prefix required).
	goSemver = regexp.MustCompile(`^v\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)
)

// imageProblem returns "" when ref names a container image by tag and digest
// (name:tag@sha256:…), as plan 08 §1.2 requires, and otherwise what is wrong.
func imageProblem(ref string) string {
	ref = strings.TrimPrefix(ref, "docker://")
	name, digest, hasDigest := strings.Cut(ref, "@")
	tag := imageTag(name)
	switch {
	case tag == "latest":
		return "must not use the floating tag \"latest\""
	case !hasDigest || !digestOnly.MatchString(digest):
		return "must be pinned by tag and digest (name:tag@sha256:…)"
	case tag == "":
		return "must carry a tag as well as a digest (name:tag@sha256:…)"
	default:
		return ""
	}
}

// imageTag returns the tag of an image name without digest ("" if none). A
// registry port (host:5000/x) is not a tag.
func imageTag(name string) string {
	i := strings.LastIndexByte(name, ':')
	if i < 0 || i < strings.LastIndexByte(name, '/') {
		return ""
	}
	return name[i+1:]
}

func imageViolation(file string, line int, what, ref string) []Violation {
	if p := imageProblem(ref); p != "" {
		return []Violation{{file, line, fmt.Sprintf("%s %q %s", what, ref, p)}}
	}
	return nil
}

// latestViolations reports "@latest" and ":latest" in non-comment text.
func latestViolations(file string, line int, text string) []Violation {
	if strings.HasPrefix(strings.TrimSpace(text), "#") {
		return nil
	}
	if latestToken.MatchString(text) {
		return []Violation{{file, line, "floating version \"latest\" is not allowed"}}
	}
	return nil
}

// exactVersion accepts one exact version, optionally written "= X.Y.Z" or
// "vX.Y.Z".
func exactVersion(v string) bool {
	v = strings.TrimSpace(v)
	v = strings.TrimSpace(strings.TrimPrefix(v, "="))
	v = strings.TrimPrefix(v, "v")
	return exactSemver.MatchString(v)
}
