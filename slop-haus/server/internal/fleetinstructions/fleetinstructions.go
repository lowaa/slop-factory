// Package fleetinstructions is the tenant's fleet instructions: named texts
// an owner writes for the agents, kept as immutable versions, one of which
// is the default every workspace is given.
//
// The default is pinned to one version, not to a name, and does not follow
// the newest version. The text ends up in every agent's prompt, so what
// reaches the fleet changes only when an owner says so -- saving a version
// can make it the default in the same request, but never does by itself.
// That also keeps "the fleet ran version N" true for whatever records it.
package fleetinstructions

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	// MaxBodyBytes keeps the text a small part of a prompt that is already
	// several thousand tokens of protocol. It matches the table's check.
	MaxBodyBytes = 20000
	MaxNameLen   = 64
)

var (
	ErrNotOwner = errors.New("fleet instructions: only the tenant's owners can change them")
	ErrInvalid  = errors.New("fleet instructions: invalid")
	ErrNotFound = errors.New("fleet instructions: no such version")
	// ErrConflict is a change the current state forbids: archiving the
	// default, or making an archived version the default.
	ErrConflict = errors.New("fleet instructions: conflict")
)

func conflict(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrConflict, fmt.Sprintf(format, args...))
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// Kind says what a text is for. Each kind has its own names, versions and
// pinned default; the rules (owner-only, immutable versions, archiving) are
// the same for all.
type Kind string

const (
	KindFleet          Kind = "fleet"
	KindSteward        Kind = "steward"
	KindSensitiveWords Kind = "sensitive_words"
)

// ParseKind reads a kind from a request; empty is fleet, which is what the
// endpoints meant before there were kinds.
func ParseKind(s string) (Kind, error) {
	switch k := Kind(s); k {
	case "":
		return KindFleet, nil
	case KindFleet, KindSteward, KindSensitiveWords:
		return k, nil
	}
	return "", invalid("unknown kind %q", s)
}

// Version is one saved text. Nothing about its text changes once saved;
// ArchivedAt only says whether the board lists it.
type Version struct {
	ID        uuid.UUID
	Name      string
	Version   int
	Kind      Kind
	Body      string
	CreatedBy *uuid.UUID
	// CreatedByDeleted says CreatedBy is nil because that person's account
	// was deleted.
	CreatedByDeleted bool
	CreatedAt        time.Time
	// ArchivedAt is nil for a version the board lists.
	ArchivedAt *time.Time
}

// Tenant is everything the board shows: every version, newest first, and
// which one is the default.
type Tenant struct {
	Versions []Version
	// DefaultID is nil while no default is chosen.
	DefaultID *uuid.UUID
}

// NormaliseName trims a name and checks it is something a person can read
// in a list and an agent in a heading: no control characters.
func NormaliseName(name string) (string, error) {
	name = strings.TrimSpace(name)
	switch {
	case name == "":
		return "", invalid("a name is required")
	case utf8.RuneCountInString(name) > MaxNameLen:
		return "", invalid("a name is at most %d characters", MaxNameLen)
	case strings.IndexFunc(name, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) >= 0:
		return "", invalid("a name cannot hold control characters")
	}
	return name, nil
}

// NormaliseBodyFor checks the text and settles its line endings on \n. Control
// characters other than newline and tab are refused: they have no use in
// instructions, and an escape sequence or NUL in a prompt file is only ever
// an attempt at something. A sensitive word list may be
// empty, since no words is a choice an owner can make; no other text may.
func NormaliseBodyFor(kind Kind, body string) (string, error) {
	if !utf8.ValidString(body) {
		return "", invalid("the text must be valid UTF-8")
	}
	body = strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\r", "\n")
	// Format characters (bidi overrides, zero-width and tag characters) are
	// refused too: they make text read differently to the person reviewing
	// it than to the model it is given to.
	if strings.IndexFunc(body, func(r rune) bool {
		return (unicode.IsControl(r) && r != '\n' && r != '\t') || unicode.Is(unicode.Cf, r)
	}) >= 0 {
		return "", invalid("the text cannot hold control or invisible formatting characters (newline and tab are fine)")
	}
	body = strings.TrimSpace(body)
	switch {
	case body == "" && kind != KindSensitiveWords:
		return "", invalid("the text is empty")
	case len(body) > MaxBodyBytes:
		return "", invalid("the text is %d bytes; the limit is %d", len(body), MaxBodyBytes)
	}
	return body, nil
}
