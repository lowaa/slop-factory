// Package score is the mutation-score ratchet: a per-package score that may
// improve but not quietly get worse, kept in a committed baseline. Its
// modes and rules follow dev-tools/quality's coverage measure.
package score

import (
	"bytes"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
)

const (
	// Tolerance is how far a package's score may fall below its baseline.
	// A package has tens of mutants, so one flaky timeout or one new
	// uncaught mutant moves its score by a point or two; gating tighter
	// would fail on noise.
	Tolerance = 2.0
	// Floor is what a package new to the baseline must reach to land, as
	// quality.sh's 80% for coverage.
	Floor = 80.0
)

// Snapshot is the score of each package, named relative to the project
// (./internal/state), to one decimal.
type Snapshot map[string]float64

// Round keeps a score to the one decimal the baseline records.
func Round(v float64) float64 { return math.Round(v*10) / 10 }

// Result is the comparison of a run with the baseline.
type Result struct {
	// Regressions fail a check.
	Regressions []string
	// Improvements pass, and --prune locks them in.
	Improvements []string
}

// Compare checks the packages cur holds against base. A baseline package
// that cur didn't run is left alone (a run may cover a subset), unless
// gone says its source no longer exists: that stale entry is an
// improvement for --prune to clear.
func Compare(base, cur Snapshot, gone func(pkg string) bool) *Result {
	res := &Result{}
	for _, pkg := range slices.Sorted(maps.Keys(cur)) {
		msg, regressed := judge(pkg, base, cur[pkg])
		switch {
		case regressed:
			res.Regressions = append(res.Regressions, msg)
		case msg != "":
			res.Improvements = append(res.Improvements, msg)
		}
	}
	for _, pkg := range stale(base, cur, gone) {
		res.Improvements = append(res.Improvements, pkg+": gone, a stale baseline entry")
	}
	return res
}

// judge says what one package's score means: a message and whether it is a
// regression, or no message when it is unchanged.
func judge(pkg string, base Snapshot, now float64) (msg string, regressed bool) {
	was, known := base[pkg]
	switch {
	case !known && now < Floor:
		return fmt.Sprintf("%s: %.1f%% is a new package under the %.0f%% floor", pkg, now, Floor), true
	case !known:
		return fmt.Sprintf("%s: new at %.1f%%", pkg, now), false
	case now < was-Tolerance:
		return fmt.Sprintf("%s: %.1f%% is more than %.1f points below the baseline %.1f%%", pkg, now, Tolerance, was), true
	case now > was:
		return fmt.Sprintf("%s: %.1f%% -> %.1f%%", pkg, was, now), false
	}
	return "", false
}

// stale lists the baseline packages that cur didn't run and whose source is
// gone.
func stale(base, cur Snapshot, gone func(pkg string) bool) []string {
	var out []string
	for _, pkg := range slices.Sorted(maps.Keys(base)) {
		if _, ran := cur[pkg]; !ran && gone != nil && gone(pkg) {
			out = append(out, pkg)
		}
	}
	return out
}

// Prune is base tightened by cur: a package's score rises to cur's, a new
// package that reached the floor is added, and a stale entry goes. It never
// loosens, so a regressed package keeps its baseline.
func Prune(base, cur Snapshot, gone func(pkg string) bool) Snapshot {
	out := maps.Clone(base)
	for _, pkg := range stale(base, cur, gone) {
		delete(out, pkg)
	}
	for pkg, c := range cur {
		b, inBase := out[pkg]
		if (inBase && c > b) || (!inBase && c >= Floor) {
			out[pkg] = c
		}
	}
	return out
}

// Rebaseline replaces the entries of the packages cur ran with its values,
// whatever they are, and keeps the rest of base (less stale entries).
func Rebaseline(base, cur Snapshot, gone func(pkg string) bool) Snapshot {
	out := Prune(base, nil, gone)
	for pkg, c := range cur {
		out[pkg] = c
	}
	return out
}

const header = "# Mutation score (killed or timed out, of decided mutants): <package> <percent>.\n" +
	"# Written by mutation.sh; see dev-tools/mutation/README.md.\n"

// Encode writes the baseline file: sorted, so its diff reads line by line.
func Encode(s Snapshot) []byte {
	var b bytes.Buffer
	b.WriteString(header)
	for _, pkg := range slices.Sorted(maps.Keys(s)) {
		fmt.Fprintf(&b, "%s %.1f\n", pkg, s[pkg])
	}
	return b.Bytes()
}

// Decode reads a baseline file.
func Decode(data []byte) (Snapshot, error) {
	out := Snapshot{}
	for i, line := range strings.Split(string(data), "\n") {
		pkg, v, err := parseLine(strings.TrimSpace(line))
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", i+1, err)
		}
		if pkg != "" {
			out[pkg] = v
		}
	}
	return out, nil
}

// parseLine reads "<package> <percent>"; a blank line or comment is no
// package.
func parseLine(line string) (pkg string, percent float64, err error) {
	if line == "" || strings.HasPrefix(line, "#") {
		return "", 0, nil
	}
	pkg, num, ok := strings.Cut(line, " ")
	percent, perr := strconv.ParseFloat(strings.TrimSpace(num), 64)
	if !ok || perr != nil || percent < 0 || percent > 100 {
		return "", 0, fmt.Errorf("want \"<package> <percent>\", got %q", line)
	}
	return pkg, percent, nil
}
