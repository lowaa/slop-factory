package gitctx

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// minGitVersionForCheckAttrSource is the first git release that understands
// `git check-attr --source=<ref>` (generatedByAttribute's way of reading
// linguist-generated from baseRef without checking it out). Older git
// rejects the flag with "unknown option", which says nothing about why --
// checked explicitly so the reviewer can say so instead.
var minGitVersionForCheckAttrSource = gitVersion{major: 2, minor: 40}

// gitVersion is the major.minor a `git version` output was parsed into.
// The patch component is never compared: no feature this package depends
// on has ever needed one.
type gitVersion struct {
	major, minor int
}

func (v gitVersion) String() string { return fmt.Sprintf("%d.%d", v.major, v.minor) }

func (v gitVersion) atLeast(min gitVersion) bool {
	if v.major != min.major {
		return v.major > min.major
	}
	return v.minor >= min.minor
}

// gitVersionRe matches the leading "git version X.Y" that every known git
// build prints, vendor suffix and patch level and all (e.g. "git version
// 2.39.2 (Apple Git-143)", "git version 2.43.0.windows.1").
var gitVersionRe = regexp.MustCompile(`^git version (\d+)\.(\d+)`)

// parseGitVersion extracts the major.minor from `git version`'s output.
func parseGitVersion(output string) (gitVersion, error) {
	trimmed := strings.TrimSpace(output)
	m := gitVersionRe.FindStringSubmatch(trimmed)
	if m == nil {
		return gitVersion{}, fmt.Errorf("%q does not look like `git version`'s output", trimmed)
	}
	major, err1 := strconv.Atoi(m[1])
	minor, err2 := strconv.Atoi(m[2])
	if err1 != nil || err2 != nil {
		// gitVersionRe only matches digits, so Atoi failing here would mean
		// an implausibly large number rather than a parsing bug.
		return gitVersion{}, fmt.Errorf("%q has a version number too large to parse", trimmed)
	}
	return gitVersion{major: major, minor: minor}, nil
}

// checkGitVersionOutput decides, from `git version`'s raw output, whether
// that git is new enough for check-attr --source -- separated from
// ensureCheckAttrSourceSupported's exec.Command call so the decision itself
// is unit-testable without a git binary.
func checkGitVersionOutput(output string) error {
	v, err := parseGitVersion(output)
	if err != nil {
		return fmt.Errorf("gitctx: checking git's version: %w", err)
	}
	if !v.atLeast(minGitVersionForCheckAttrSource) {
		return fmt.Errorf("slop-review needs git %s or newer (found git %s)", minGitVersionForCheckAttrSource, v)
	}
	return nil
}

// checkAttrSourceOnce and checkAttrSourceErr cache ensureCheckAttrSourceSupported's
// result for the life of the process: the git on PATH doesn't change
// between the calls one review makes, and generatedByAttribute runs once
// per file set but Show/Log/Blame-style repeat invocations (bench, say)
// would otherwise shell out to `git version` on every one.
var (
	checkAttrSourceOnce sync.Once
	checkAttrSourceErr  error
)

// ensureCheckAttrSourceSupported fails clearly, before generatedByAttribute
// runs check-attr --source and gets back git's own unhelpful "unknown
// option" error, when the git on PATH predates --source's introduction in
// git 2.40.
func ensureCheckAttrSourceSupported(repoRoot string) error {
	checkAttrSourceOnce.Do(func() {
		out, err := runGit(repoRoot, "version")
		if err != nil {
			checkAttrSourceErr = fmt.Errorf("gitctx: checking git version: %w", err)
			return
		}
		checkAttrSourceErr = checkGitVersionOutput(out)
	})
	return checkAttrSourceErr
}
