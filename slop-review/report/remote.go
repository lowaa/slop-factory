package report

import (
	"fmt"
	"net/url"
	"path"
	"strings"
)

// RemoteKey reduces a git remote URL to the identity of the repository it
// names: host and path, lowercased, without scheme, user, port or a ".git"
// suffix. Every way of writing the same remote maps to one key --
//
//	git@github.com:example/widgets.git
//	ssh://git@github.com:22/example/widgets
//	https://github.com/example/widgets.git
//
// all become "github.com/example/widgets" -- which is what makes two
// clones of one repo, checked out by different agents in different ways, the
// same repo to the server. The server computes it itself rather than trusting
// a key sent by the client.
func RemoteKey(remote string) (string, error) {
	raw := strings.TrimSpace(remote)
	if raw == "" {
		return "", fmt.Errorf("empty git remote")
	}
	var host, p string
	switch {
	case strings.Contains(raw, "://"):
		u, err := url.Parse(raw)
		if err != nil {
			// url.Parse's own error embeds the input it was given, which may
			// be exactly the credential-bearing string that got us here --
			// so it is not wrapped, only named.
			return "", fmt.Errorf("git remote is not a valid URL")
		}
		if u.Scheme == "file" {
			return "", fmt.Errorf("git remote %q is a local path, which names no shared repository", RedactRemote(remote))
		}
		host, p = u.Hostname(), u.Path
	case strings.Contains(raw, ":") && !strings.HasPrefix(raw, "/"):
		// scp-like syntax: [user@]host:path
		hostPart, pathPart, _ := strings.Cut(raw, ":")
		if at := strings.LastIndex(hostPart, "@"); at >= 0 {
			hostPart = hostPart[at+1:]
		}
		host, p = hostPart, pathPart
	default:
		return "", fmt.Errorf("git remote %q is a local path, which names no shared repository", RedactRemote(remote))
	}

	host = strings.ToLower(strings.TrimSpace(host))
	p = strings.Trim(path.Clean("/"+p), "/")
	p = strings.TrimSuffix(p, ".git")
	if host == "" || p == "" || p == "." {
		return "", fmt.Errorf("git remote %q names no repository", RedactRemote(remote))
	}
	return host + "/" + strings.ToLower(p), nil
}

// RedactRemote strips any embedded credential from a git remote so it is
// safe to log, store, or show to another user. A URL-form remote
// (scheme://...) has its userinfo removed entirely -- even a bare username
// can itself be a token, as in https://ghp_xxx@github.com/o/r. An scp-like
// remote (git@host:path) is returned unchanged (TrimSpace'd): the ssh user
// it names is not a secret. A URL-form remote that fails to parse is never
// echoed back -- it may be exactly the credential-bearing string that made
// redaction necessary -- so it becomes "" instead.
func RedactRemote(remote string) string {
	raw := strings.TrimSpace(remote)
	if !strings.Contains(raw, "://") {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	u.User = nil
	return u.String()
}

// RepoName is the display name for a remote key: its last path segment.
func RepoName(key string) string {
	return path.Base(key)
}
