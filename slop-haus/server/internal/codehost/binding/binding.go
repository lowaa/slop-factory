package binding

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
)

type Kind int

const (
	KindHost Kind = iota + 1
	KindOwner
	KindRepo
)

func (k Kind) String() string {
	switch k {
	case KindHost:
		return "host"
	case KindOwner:
		return "owner"
	case KindRepo:
		return "repo"
	}
	return "invalid"
}

var ErrInvalid = errors.New("invalid binding")

// Normalize validates a binding and returns its canonical (lowercased) form
// and kind. A binding is "host", "host/owner" or "host/owner/repo", the same
// host-qualified form as a manifest remote. Hosts treat owners and repos as
// case-insensitive, so folding here keeps one credential from being stored
// twice under different spellings.
func Normalize(raw string) (string, Kind, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	segs := strings.Split(s, "/")
	if len(segs) > 3 {
		return "", 0, fmt.Errorf("%w: %q has more than host/owner/repo", ErrInvalid, raw)
	}
	for _, seg := range segs {
		if seg == "" {
			return "", 0, fmt.Errorf("%w: %q has an empty segment", ErrInvalid, raw)
		}
		if seg == "." || seg == ".." {
			return "", 0, fmt.Errorf("%w: %q has a relative segment", ErrInvalid, raw)
		}
		if strings.IndexFunc(seg, unicode.IsSpace) >= 0 || strings.ContainsAny(seg, `\:@?#%*`) {
			return "", 0, fmt.Errorf("%w: %q has an invalid character", ErrInvalid, raw)
		}
	}
	return s, Kind(len(segs)), nil
}

// Resolve returns the most specific binding that covers repo (a
// "host/owner/repo" remote): repo over owner over host. Matching is by whole
// path segment, so "github.com/acme" does not cover "github.com/acme-corp/x".
// Invalid bindings are skipped; ok is false when none covers repo or repo is
// not a full remote.
func Resolve(bindings []string, repo string) (binding string, ok bool) {
	r, kind, err := Normalize(repo)
	if err != nil || kind != KindRepo {
		return "", false
	}
	best := Kind(0)
	for _, b := range bindings {
		n, k, err := Normalize(b)
		if err != nil || k <= best {
			continue
		}
		if r == n || strings.HasPrefix(r, n+"/") {
			binding, best = n, k
		}
	}
	return binding, best != 0
}
