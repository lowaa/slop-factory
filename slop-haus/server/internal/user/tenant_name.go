package user

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

// MaxTenantNameLength is counted in runes: a name in a script that takes
// several bytes per character is not shorter than one in ASCII.
const MaxTenantNameLength = 40

var (
	ErrTenantNameEmpty   = errors.New("user: a workspace name can't be empty")
	ErrTenantNameTooLong = errors.New("user: a workspace name can be at most 40 characters")
	ErrTenantNameInvalid = errors.New("user: a workspace name can't contain line breaks or control characters")
	// ErrTenantNameTaken is only ever about the caller's own tenants:
	// names are not unique across the board, and saying that another
	// person's tenant has a name would reveal that it exists.
	ErrTenantNameTaken = errors.New("user: you already belong to a workspace with that name")
)

// ValidateTenantName is the one rule for naming a tenant, applied by every
// path that names one. It returns the name as it is stored. That the name
// doesn't clash with another of the person's tenants is checked by the
// store, in the same transaction that writes it.
func ValidateTenantName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", ErrTenantNameEmpty
	}
	for _, r := range name {
		// Cc is the control characters; Zl and Zp are the Unicode line and
		// paragraph separators, which break a line like a newline does.
		if unicode.IsControl(r) || unicode.In(r, unicode.Zl, unicode.Zp) {
			return "", ErrTenantNameInvalid
		}
	}
	if utf8.RuneCountInString(name) > MaxTenantNameLength {
		return "", ErrTenantNameTooLong
	}
	return name, nil
}

// NewTenant is the tenant an accepted new_tenant invite makes. An empty
// Name means "named after the person", as activation does.
type NewTenant struct {
	ID   uuid.UUID
	Name string
}

// defaultTenantName is the name a tenant gets when its owner gave none: the
// owner's display name made to fit the rules, since display names may be
// longer, and are less restricted, than tenant names.
func defaultTenantName(displayName string) string {
	clean := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.In(r, unicode.Zl, unicode.Zp) {
			return ' '
		}
		return r
	}, displayName)
	name := strings.Join(strings.Fields(clean), " ")
	if r := []rune(name); len(r) > MaxTenantNameLength {
		name = strings.TrimSpace(string(r[:MaxTenantNameLength]))
	}
	if name == "" {
		return "Workspace"
	}
	return name
}
