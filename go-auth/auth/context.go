package auth

import (
	"context"

	"github.com/google/uuid"
)

type userIDContextKey struct{}

// ContextWithUserID and UserIDFromContext mirror go-db/tenancy's
// context-key pattern for storing the authenticated user's ID, resolved by
// callers' own auth middleware from the access token's claims.
func ContextWithUserID(ctx context.Context, userID uuid.UUID) context.Context {
	return context.WithValue(ctx, userIDContextKey{}, userID)
}

func UserIDFromContext(ctx context.Context) (uuid.UUID, bool) {
	id, ok := ctx.Value(userIDContextKey{}).(uuid.UUID)
	return id, ok
}
