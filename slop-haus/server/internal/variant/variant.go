// Package variant is the tenant's variants: named configurations an agent
// can run on -- a harness, its settings and a fleet-instructions version --
// kept as immutable versions.
//
// A variant is not an agent role: a role says what an agent can do, a
// variant what it runs. Saving under a name adds its next version and never
// changes an earlier one, so "ran with variant X v2" stays one
// configuration for whatever records it later.
package variant

import (
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/harness"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/harnessconfig"
)

const MaxNameLen = 64

var (
	ErrNotOwner = errors.New("variants: only the tenant's owners can use them")
	ErrInvalid  = errors.New("variants: invalid")
	ErrNotFound = errors.New("variants: no such version")
	// ErrAgentNotFound is an agent the tenant does not have.
	ErrAgentNotFound = errors.New("variants: no such agent")
)

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// Version is one saved configuration. Nothing about it changes once saved;
// ArchivedAt only says whether the board lists it.
type Version struct {
	ID      uuid.UUID
	Name    string
	Version int
	Harness string
	// Settings are the harness's own, name to value.
	Settings map[string]string
	// FleetInstructionsVersionID is nil for explicitly no fleet
	// instructions, which is not the same as the tenant's default.
	FleetInstructionsVersionID *uuid.UUID
	CreatedBy                  *uuid.UUID
	// CreatedByDeleted says CreatedBy is nil because that person's account
	// was deleted.
	CreatedByDeleted bool
	CreatedAt        time.Time
	// ArchivedAt is nil for a version the board lists.
	ArchivedAt *time.Time
}

// NormaliseName trims a name and checks it is something a person can read
// in a list: no control or invisible formatting characters.
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

// ValidateSettings checks harness is one harnessconfig knows and that
// settings are known, valid settings of that harness alone. The definitions
// and patterns are harnessconfig's, so a setting added there is accepted
// here without a change. It returns a copy.
func ValidateSettings(name string, settings map[string]string) (map[string]string, error) {
	if !harness.Valid(name) {
		return nil, invalid("harness must be one of %s", strings.Join(harness.Names(), ", "))
	}
	// Values names the harness in each setting's key, so a setting of the
	// other harness is only ever "unknown setting <harness>.<name>".
	if err := (harnessconfig.Values{name: settings}).Validate(); err != nil {
		return nil, invalid("%s", err.Error())
	}
	// Never nil: it is stored as a JSON object, and null is not one.
	return maps.Collect(maps.All(settings)), nil
}
