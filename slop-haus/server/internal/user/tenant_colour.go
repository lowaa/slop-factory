package user

import (
	"errors"
	"slices"
)

// TenantColours are the keys a tenant's owner can choose from. The frontend
// owns the colour values (light and dark) for each; this is the list the
// server accepts, and the openapi enum the generated client takes its type
// from.
var TenantColours = []string{"red", "orange", "amber", "green", "teal", "blue", "violet", "pink"}

var ErrTenantColourUnknown = errors.New("user: that is not one of the workspace colours")

// ValidateTenantColour accepts nil, which clears the colour back to the
// default derived from the tenant's id.
func ValidateTenantColour(colour *string) error {
	if colour != nil && !slices.Contains(TenantColours, *colour) {
		return ErrTenantColourUnknown
	}
	return nil
}
