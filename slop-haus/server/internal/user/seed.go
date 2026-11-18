package user

import (
	"context"
	"strings"

	"github.com/google/uuid"
)

// Seeder creates accounts that are active from the start, skipping the
// emailed link. It is kept apart from Service so that nothing a request
// can reach holds one: it exists for seeding development and test
// environments from the command line.
type Seeder struct {
	store Store
}

func NewSeeder(store Store) *Seeder {
	return &Seeder{store: store}
}

// EnsureActive creates the account, with a tenant of its own, unless the
// address is already taken -- in which case nothing changes, the password
// included, so running it again is harmless.
func (s *Seeder) EnsureActive(ctx context.Context, in *RegisterInput) (created bool, err error) {
	hash, err := hashPassword(in.Password)
	if err != nil {
		return false, err
	}
	u := &User{ID: uuid.New(), Email: normalizeEmail(in.Email), PasswordHash: hash, DisplayName: strings.TrimSpace(in.DisplayName)}
	return s.store.EnsureActive(ctx, u, uuid.New())
}

// MakeAdmin makes an existing account a server admin, who can invite
// people to start tenants of their own. Idempotent.
func (s *Seeder) MakeAdmin(ctx context.Context, email string) error {
	return s.store.SetAdmin(ctx, normalizeEmail(email))
}
