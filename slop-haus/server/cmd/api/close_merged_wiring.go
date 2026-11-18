package main

import (
	"context"

	"github.com/google/uuid"
)

// displayNamer is what the user service offers for naming a person.
type displayNamer interface {
	DisplayName(ctx context.Context, userID uuid.UUID) (string, error)
}

// userNameAdapter lets the ticket service name who closed a ticket as merged
// without depending on the user package.
type userNameAdapter struct{ users displayNamer }

func (a userNameAdapter) UserName(ctx context.Context, id uuid.UUID) (string, error) {
	return a.users.DisplayName(ctx, id)
}
