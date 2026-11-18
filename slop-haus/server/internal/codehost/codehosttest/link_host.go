package codehosttest

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
)

// NewLinkHost is a Fake for tests that file many changes and only need a
// link to name one: https://{domain}/{owner}/{repo}/pull/{n}, with anything
// after the number ignored, and the remote {domain}/{owner}/{repo}. Names
// are lower-cased. It is a fixture's stand-in, not a model of any host's
// links: what a real host reads is tested in its own package.
func NewLinkHost(domain string) *Fake {
	f := &Fake{Domain: domain}
	f.ParseURLFunc = func(raw string) (codehost.ChangeRef, bool) {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Host, domain) {
			return codehost.ChangeRef{}, false
		}
		segs := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
		if len(segs) < 4 || segs[0] == "" || segs[1] == "" || segs[2] != "pull" {
			return codehost.ChangeRef{}, false
		}
		n, err := strconv.ParseInt(segs[3], 10, 64)
		if err != nil || n <= 0 {
			return codehost.ChangeRef{}, false
		}
		return codehost.ChangeRef{Host: domain, Owner: strings.ToLower(segs[0]), Repo: strings.ToLower(segs[1]), Number: n}, true
	}
	f.ChangeURLFunc = func(r codehost.ChangeRef) string {
		return fmt.Sprintf("https://%s/%s/%s/pull/%d", domain, r.Owner, r.Repo, r.Number)
	}
	f.ParseRemoteFunc = func(remote string) (codehost.Repo, bool) {
		segs := strings.Split(remote, "/")
		if len(segs) != 3 || !strings.EqualFold(segs[0], domain) || segs[1] == "" || segs[2] == "" {
			return codehost.Repo{}, false
		}
		return codehost.Repo{Owner: strings.ToLower(segs[1]), Name: strings.ToLower(segs[2])}, true
	}
	return f
}

// Registry is a codehost.Registry with hosts registered, each for reading
// and acting alike.
func Registry(hosts ...*Fake) *codehost.Registry {
	reg := codehost.NewRegistry()
	for _, h := range hosts {
		reg.Register(h, h)
	}
	return reg
}
