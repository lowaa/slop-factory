package scope

import (
	"errors"
	"path"
	"strings"
)

// ErrInvalidPath is returned by NormalizePath for input that can't be
// resolved to a repo-relative path.
var ErrInvalidPath = errors.New("ticket: invalid path scope")

// NormalizePath puts a path scope into the one form Conflicts can compare:
// repo-relative, forward slashes, no "./" or ".." segments, and a trailing
// "/" if and only if the scope names a directory.
//
// The trailing slash is load-bearing rather than cosmetic -- it is the only
// thing distinguishing the directory "core/" (which contains things) from
// the file "core" (which contains nothing), and Conflicts relies on it to
// avoid deciding that "core/" contains "coreutil/".
func NormalizePath(raw string) (string, error) {
	p := strings.TrimSpace(raw)
	if p == "" {
		return "", ErrInvalidPath
	}
	p = strings.ReplaceAll(p, "\\", "/")

	isDir := strings.HasSuffix(p, "/")

	// path.Clean resolves "." and ".." and strips duplicate slashes, but
	// also drops any trailing slash, hence capturing isDir first.
	cleaned := path.Clean(p)
	cleaned = strings.TrimPrefix(cleaned, "/")

	// A scope that escapes the repo root, or cleans away to nothing, is not
	// something the conflict rule can reason about.
	if cleaned == "" || cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", ErrInvalidPath
	}

	if isDir {
		cleaned += "/"
	}
	return cleaned, nil
}

// Conflicts reports whether two normalized path scopes overlap -- that is,
// whether granting both to different agents would let them write the same
// file.
//
// The rule is deliberately delimiter-aware rather than a raw string prefix
// test. Plain prefixing makes "server/main.go" a prefix of
// "server/main.go.bak", and "core" a prefix of "coreutil" -- both false
// conflicts. Only a directory scope can contain anything, so containment is
// only ever tested against a scope that ends in "/".
//
// It is also not a SQL LIKE pattern. "_" and "%" are LIKE metacharacters
// and "_" appears in almost every filename in this repo, so
// `a LIKE b || '%'` would report "test_file.go" as conflicting with
// "testXfile.go". False conflicts fail safe -- work is withheld, never
// double-granted -- which is exactly why they would go unnoticed.
func Conflicts(a, b string) bool {
	if a == b {
		return true
	}
	if strings.HasSuffix(a, "/") && strings.HasPrefix(b, a) {
		return true
	}
	if strings.HasSuffix(b, "/") && strings.HasPrefix(a, b) {
		return true
	}
	return false
}

// ConflictsAny reports whether scope conflicts with any of others, and
// which one. The second return is the first conflicting scope, for error
// messages that name what actually blocked the claim.
func ConflictsAny(scope string, others []string) (bool, string) {
	for _, other := range others {
		if Conflicts(scope, other) {
			return true, other
		}
	}
	return false, ""
}

// Why this comparison lives in Go rather than in SQL: at slop-haus's scale
// (a handful of active leases, tens of open tickets) the claim path already
// holds an advisory lock and can simply load the active scopes and compare
// them here. Pushing the predicate into SQL buys nothing measurable and
// costs the LIKE-metacharacter hazard above, plus a rule expressed in two
// languages that have to agree. If this ever needs an index to stay fast,
// the normalized storage makes an ltree/GiST migration available -- but
// that is a different trade, made deliberately, not a default.
