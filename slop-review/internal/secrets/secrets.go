// Package secrets is the shared secret-shaped-filename matcher, used both
// to gate the read-only toolset (internal/toolset) and to redact the
// initial diff sent to the model (internal/gitctx). It's its own leaf
// package -- rather than living in either of those -- so gitctx and
// toolset (which already imports gitctx for its git_show/log/blame tools)
// can both depend on it without an import cycle.
package secrets

import "path/filepath"

// patterns are filepath.Match patterns (matched against the base filename)
// for paths that commonly hold real secrets.
var patterns = []string{
	".env", ".env.*",
	"*.pem", "*.key", "*.pfx", "*.p12",
	"id_rsa", "id_rsa.*", "id_ed25519", "id_ed25519.*",
	"credentials", "credentials.json", "*_credentials*",
	".npmrc", ".netrc",
}

// LooksLikeSecret reports whether path's base filename matches one of
// patterns.
func LooksLikeSecret(path string) bool {
	base := filepath.Base(path)
	for _, pattern := range patterns {
		if ok, _ := filepath.Match(pattern, base); ok {
			return true
		}
	}
	return false
}
