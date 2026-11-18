package ticket_test

import (
	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
)

// testPerson is a signed-in person for calls that need only a person, not
// a particular one.
func testPerson() *authn.User {
	return &authn.User{ID: uuid.New(), TenantID: uuid.New()}
}
