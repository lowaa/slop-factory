package user

import (
	"context"
	"log/slog"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// TenantCreatedHook is what a freshly made tenant needs done to it that the
// user package does not own (its starting settings). It is an interface,
// injected from main, so this package does not import the packages it reaches.
type TenantCreatedHook interface {
	TenantCreated(ctx context.Context, tenantID, ownerID uuid.UUID) error
}

// StoreOption configures a PostgresStore.
type StoreOption func(*PostgresStore)

// WithTenantCreatedHook runs h after each tenant is committed, whichever path
// made it. A failure is logged and does not undo the tenant: the person has
// already been let in, and the hook's work can be done by hand.
func WithTenantCreatedHook(h TenantCreatedHook, logHandler slog.Handler) StoreOption {
	return func(s *PostgresStore) { s.onTenantCreated, s.logger = h, slog.New(logHandler) }
}

type madeTenant struct{ tenantID, ownerID uuid.UUID }

type madeTenantsKey struct{}

// noteMadeTenant records, for inTenantTx to report once the transaction has
// committed, that provisionTenant made a tenant in it.
func noteMadeTenant(ctx context.Context, tenantID, ownerID uuid.UUID) {
	if made, ok := ctx.Value(madeTenantsKey{}).(*[]madeTenant); ok {
		*made = append(*made, madeTenant{tenantID, ownerID})
	}
}

// inTenantTx is InGlobalTx for a transaction that may make a tenant. fn must
// use the ctx it is given, which is how provisionTenant reports into it. The
// hook runs only after the commit, since it reads the tenant through other
// connections.
func (s *PostgresStore) inTenantTx(ctx context.Context, fn func(ctx context.Context, q db.Querier) error) error {
	var made []madeTenant
	txCtx := context.WithValue(ctx, madeTenantsKey{}, &made)
	if err := s.db.InGlobalTx(txCtx, func(q db.Querier) error { return fn(txCtx, q) }); err != nil {
		return err
	}
	if s.onTenantCreated == nil {
		return nil
	}
	for _, m := range made {
		if err := s.onTenantCreated.TenantCreated(ctx, m.tenantID, m.ownerID); err != nil {
			s.logger.Error("tenant created hook failed", "tenant", m.tenantID, "err", err)
		}
	}
	return nil
}
