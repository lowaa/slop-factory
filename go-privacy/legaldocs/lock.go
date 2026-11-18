package legaldocs

import (
	"bufio"
	"bytes"
	"fmt"
	"slices"
	"strings"
)

// Lock is the contents of an app's versions.lock: what each document version
// looked like when it was first added, so CI can refuse a change to a
// published one without a database.
//
// Every version is locked on its source (SourceHash): the file as written,
// with its {{inventory:...}} directives unexpanded. That is what a person
// editing the file changes, and it does not move when the inventory does.
//
// Only the head of each kind and locale, the version in force once its date
// comes (the latest by (effective, version)), is also locked on its rendered
// text (ContentHash). An inventory change that alters what the head says has
// to ship as a new version, so this fails it. Older versions are not locked
// that way: their generated sections would drift with every inventory change
// although nothing published changed. What was published is the database's
// row, and Service.Publish never compares it with a render.
type Lock struct {
	entries map[lockKey]LockEntry
}

// LockEntry is what a lock line records for one version.
type LockEntry struct {
	Source   string
	Rendered string
}

type lockKey struct{ kind, version, locale string }

func keyOf(v *Version) lockKey { return lockKey{v.Kind, v.Version, v.Locale} }

// LockLine is the line for v: kind, version, locale, source hash and rendered
// hash, space separated.
func LockLine(v *Version) string {
	return fmt.Sprintf("%s %s %s %s %s", v.Kind, v.Version, v.Locale, v.SourceHash, v.ContentHash())
}

// ParseLock reads a lock file. Blank lines and lines starting with # are
// skipped.
func ParseLock(data []byte) (*Lock, error) {
	l := &Lock{entries: map[lockKey]LockEntry{}}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) != 5 {
			return nil, fmt.Errorf("legaldocs: lock line %d: want <kind> <version> <locale> <source sha256> <rendered sha256>", n)
		}
		k := lockKey{f[0], f[1], f[2]}
		if _, dup := l.entries[k]; dup {
			return nil, fmt.Errorf("legaldocs: lock line %d: %s %s (%s) is locked twice", n, k.kind, k.version, k.locale)
		}
		l.entries[k] = LockEntry{Source: f[3], Rendered: f[4]}
	}
	return l, sc.Err()
}

// heads is the version in force, per kind and locale, among versions: the
// latest by (effective, version), as Service orders them. A version scheduled
// for later counts: it is the one an inventory change must not alter either.
func heads(versions []*Version) map[[2]string]*Version {
	out := map[[2]string]*Version{}
	for _, v := range versions {
		k := [2]string{v.Kind, v.Locale}
		cur := out[k]
		if cur == nil || v.Effective.After(cur.Effective) || (v.Effective.Equal(cur.Effective) && v.Version > cur.Version) {
			out[k] = v
		}
	}
	return out
}

// Unlocked is the versions with no lock line yet, in the order given.
func (l *Lock) Unlocked(versions []*Version) []*Version {
	var out []*Version
	for _, v := range versions {
		if _, ok := l.entries[keyOf(v)]; !ok {
			out = append(out, v)
		}
	}
	return out
}

// Check says what in versions disagrees with the lock, one message each:
// a locked version whose source changed, the head whose render changed, and a
// locked version that is gone. A version with no line is not a problem here
// (see Unlocked).
func (l *Lock) Check(versions []*Version) []string {
	var problems []string
	head := heads(versions)
	present := map[lockKey]bool{}
	for _, v := range versions {
		k := keyOf(v)
		present[k] = true
		e, ok := l.entries[k]
		if !ok {
			continue
		}
		id := fmt.Sprintf("%s %s (%s)", v.Kind, v.Version, v.Locale)
		if e.Source != v.SourceHash {
			problems = append(problems, id+" is published and its file changed: add a new version file instead of editing it")
		}
		if head[[2]string{v.Kind, v.Locale}] == v && e.Rendered != v.ContentHash() {
			problems = append(problems, id+" is the version in force and what it renders changed (the inventory moved under it): add a new version file")
		}
	}
	for k := range l.entries {
		if !present[k] {
			problems = append(problems, fmt.Sprintf("%s %s (%s) is locked but no longer exists: a published version is never removed", k.kind, k.version, k.locale))
		}
	}
	slices.Sort(problems)
	return problems
}
