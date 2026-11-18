// Package chunk splits a large change set into pieces small enough for one
// review session each, without separating the files a reviewer needs to
// see together.
package chunk

import (
	"path"
	"sort"
	"strings"

	"github.com/lowaa/slop-factory/slop-review/internal/gitctx"
)

// Chunk is one session's share of the change: the paths it owns, in
// review order, and the diff bytes they add up to.
type Chunk struct {
	Files []string
	Bytes int
}

// Whole puts every reviewable file in one chunk.
func Whole(files []gitctx.FileDiff) []Chunk {
	c := Chunk{}
	for _, f := range planned(files) {
		c.Files = append(c.Files, f.Path)
		c.Bytes += f.Size()
	}
	return []Chunk{c}
}

// ByPackage groups files by directory -- a test file joining the file it
// tests, wherever that lives -- and packs consecutive groups into chunks
// of at most budget bytes. A group bigger than budget is split on its own,
// keeping each test next to its implementation where it can.
//
// Groups are packed in path order rather than by size (a tighter bin
// packing): neighbouring directories are usually related, so path order
// keeps what little does cross a package boundary in one session, and
// makes the plan stable and predictable from the file list alone.
func ByPackage(files []gitctx.FileDiff, budget int) []Chunk {
	candidates := planned(files)
	groups := groupByPackage(candidates)

	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var chunks []Chunk
	var current Chunk
	flush := func() {
		if len(current.Files) > 0 {
			chunks = append(chunks, current)
			current = Chunk{}
		}
	}
	for _, k := range keys {
		group := groups[k]
		size := 0
		for _, f := range group {
			size += f.Size()
		}

		if size > budget {
			flush()
			for _, f := range group {
				if len(current.Files) > 0 && current.Bytes+f.Size() > budget {
					flush()
				}
				current.Files = append(current.Files, f.Path)
				current.Bytes += f.Size()
			}
			flush()
			continue
		}

		if len(current.Files) > 0 && current.Bytes+size > budget {
			flush()
		}
		for _, f := range group {
			current.Files = append(current.Files, f.Path)
		}
		current.Bytes += size
	}
	flush()
	return chunks
}

// planned is the files worth a session's attention. A change made only of
// noise (a lone go.sum bump, say) still gets reviewed rather than silently
// approved, so noise is kept when there is nothing else.
func planned(files []gitctx.FileDiff) []*gitctx.FileDiff {
	var reviewable, all []*gitctx.FileDiff
	for i := range files {
		all = append(all, &files[i])
		if files[i].Reviewable() {
			reviewable = append(reviewable, &files[i])
		}
	}
	if len(reviewable) == 0 {
		return all
	}
	return reviewable
}

// groupByPackage keys each file by its directory, except that a test file
// is keyed by the directory of the file it tests, when that file is part
// of the change -- a test reviewed apart from its implementation can't be
// judged by anyone, least of all a test-efficacy reviewer. Within a group,
// files are ordered so each implementation is followed by its tests.
func groupByPackage(files []*gitctx.FileDiff) map[string][]*gitctx.FileDiff {
	byBase := map[string][]*gitctx.FileDiff{}
	for _, f := range files {
		byBase[path.Base(f.Path)] = append(byBase[path.Base(f.Path)], f)
	}

	groups := map[string][]*gitctx.FileDiff{}
	pairKey := map[*gitctx.FileDiff]string{}
	for _, f := range files {
		key := path.Dir(f.Path)
		pairKey[f] = f.Path
		if impl, ok := testedFile(f.Path, byBase); ok {
			key = path.Dir(impl.Path)
			pairKey[f] = impl.Path + "\x00" + f.Path
		}
		groups[key] = append(groups[key], f)
	}
	for _, group := range groups {
		sort.SliceStable(group, func(i, j int) bool { return pairKey[group[i]] < pairKey[group[j]] })
	}
	return groups
}

// testedFile finds the implementation a test file tests, among the changed
// files, by the naming conventions this repo's languages use: preferring
// one in the test's own directory, else a unique match elsewhere. A
// basename shared by several changed files elsewhere is ambiguous and left
// unpaired, since a wrong guess is worse than none.
func testedFile(testPath string, byBase map[string][]*gitctx.FileDiff) (*gitctx.FileDiff, bool) {
	base := path.Base(testPath)
	implBase, ok := implementationName(base)
	if !ok {
		return nil, false
	}
	candidates := byBase[implBase]
	sameDir := path.Join(path.Dir(testPath), implBase)
	for _, c := range candidates {
		if c.Path == sameDir {
			return c, true
		}
	}
	// A Go test always lives in its package's directory, so a same-named
	// file elsewhere is a coincidence -- settings/main_test.go is not a
	// test of cmd/api/main.go.
	if len(candidates) != 1 || path.Ext(base) == ".go" {
		return nil, false
	}
	return candidates[0], true
}

// implementationName maps a test file's basename to its implementation's:
// foo_test.go, foo_test.py, test_foo.py, foo.test.ts(x), foo.spec.ts(x)
// (and .js/.jsx).
func implementationName(base string) (string, bool) {
	ext := path.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	switch {
	case strings.HasSuffix(stem, "_test"):
		return strings.TrimSuffix(stem, "_test") + ext, true
	case ext == ".py" && strings.HasPrefix(stem, "test_"):
		return strings.TrimPrefix(stem, "test_") + ext, true
	case strings.HasSuffix(stem, ".test"):
		return strings.TrimSuffix(stem, ".test") + ext, true
	case strings.HasSuffix(stem, ".spec"):
		return strings.TrimSuffix(stem, ".spec") + ext, true
	}
	return "", false
}
