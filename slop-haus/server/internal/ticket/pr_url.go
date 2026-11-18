package ticket

import (
	"fmt"
	"net/url"
	"strings"
	"unicode"
)

// maxPRURLLen matches the schema's CHECK on tickets.pr_url.
const maxPRURLLen = 2048

// DoneParams is what an agent reports when it finishes a ticket.
type DoneParams struct {
	// Summary is recorded as the agent's comment; empty records none.
	Summary string
	// PRURL is the reviewable change the work was merged through. Optional:
	// empty records none.
	PRURL string
}

// parsePRURL validates a reported PR link, returning nil for none.
//
// The link is opaque to slop-haus -- it never calls the host -- so the only
// rules are the ones that make it safe to render as a link: https, a host,
// no credentials, nothing a browser would parse differently from what a
// human reads. It is stored exactly as given, not re-serialised, so what the
// agent reported is what the board shows.
func parsePRURL(raw string) (*string, error) {
	if raw == "" {
		return nil, nil
	}
	if len(raw) > maxPRURLLen {
		return nil, fmt.Errorf("%w: longer than %d bytes", ErrInvalidPRURL, maxPRURLLen)
	}
	// Whitespace and control characters are stripped or tolerated
	// differently by browsers and parsers; refusing them outright means the
	// link a human reads is the one they follow.
	if strings.ContainsFunc(raw, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return nil, fmt.Errorf("%w: contains whitespace or control characters", ErrInvalidPRURL)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidPRURL, err)
	}
	// url.Parse lowercases the scheme, so HTTPS:// passes here but not the
	// schema's case-sensitive CHECK; require the canonical form instead.
	if !strings.HasPrefix(raw, "https://") || u.Scheme != "https" {
		return nil, fmt.Errorf("%w: got %q", ErrInvalidPRURL, raw)
	}
	if u.Hostname() == "" {
		return nil, fmt.Errorf("%w: no host", ErrInvalidPRURL)
	}
	if u.User != nil {
		return nil, fmt.Errorf("%w: must not carry credentials", ErrInvalidPRURL)
	}
	return &raw, nil
}
