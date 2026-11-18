package authn_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

type fakeRoles struct {
	owner, member bool
	err           error
}

func (f fakeRoles) IsTenantOwner(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
	return f.owner, f.err
}

func (f fakeRoles) IsTenantMember(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
	return f.member, f.err
}

func TestOwnerGate(t *testing.T) {
	who := &authn.User{ID: uuid.New(), TenantID: uuid.New()}
	boom := errors.New("boom")
	cases := []struct {
		name      string
		roles     authn.TenantOwners
		who       *authn.User
		wantErr   error
		wantAudit bool
	}{
		{"agent", fakeRoles{owner: true}, nil, authn.ErrNotPerson, false},
		{"non-member", fakeRoles{}, who, authn.ErrNotOwner, true},
		{"member", fakeRoles{member: true}, who, authn.ErrNotOwner, true},
		{"owner", fakeRoles{owner: true, member: true}, who, nil, false},
		{"nil roles", nil, who, authn.ErrNotOwner, false},
		{"lookup error", fakeRoles{err: boom}, who, boom, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var audited [][2]uuid.UUID
			g := authn.NewOwnerGate(c.roles, authn.WithAudit(func(_ context.Context, u, tn uuid.UUID) {
				audited = append(audited, [2]uuid.UUID{u, tn})
			}))
			err := g.RequireOwner(context.Background(), c.who)
			if c.wantErr == nil {
				assert.NoError(t, err)
			} else {
				assert.ErrorIs(t, err, c.wantErr)
			}
			if c.wantAudit {
				assert.Equal(t, [][2]uuid.UUID{{who.ID, who.TenantID}}, audited)
			} else {
				assert.Empty(t, audited)
			}
		})
	}
}

func TestOwnerGateMember(t *testing.T) {
	who := &authn.User{ID: uuid.New(), TenantID: uuid.New()}
	ctx := context.Background()
	assert.ErrorIs(t, authn.NewMemberGate(fakeRoles{}).RequireMemberOf(ctx, who.ID, who.TenantID), authn.ErrNotMember)
	assert.ErrorIs(t, authn.NewMemberGate(nil).RequireMemberOf(ctx, who.ID, who.TenantID), authn.ErrNotMember)
	assert.NoError(t, authn.NewMemberGate(fakeRoles{member: true}).RequireMemberOf(ctx, who.ID, who.TenantID))
}

func TestOwnerGateInContext(t *testing.T) {
	person := uuid.New()
	tenant := uuid.New()
	scoped := db.WithTenant(context.Background(), tenant)
	owner := authn.NewOwnerGate(fakeRoles{owner: true})
	assert.NoError(t, owner.RequireOwnerInContext(scoped, person))
	assert.ErrorIs(t, owner.RequireOwnerInContext(context.Background(), person), authn.ErrNotOwner)
	assert.ErrorIs(t, authn.NewOwnerGate(fakeRoles{}).RequireOwnerInContext(scoped, person), authn.ErrNotOwner)
	assert.ErrorIs(t, authn.NewOwnerGate(nil).RequireOwnerInContext(scoped, person), authn.ErrNotOwner)
}

func TestMemberGateLookupErrorIsNotAudited(t *testing.T) {
	who := &authn.User{ID: uuid.New(), TenantID: uuid.New()}
	boom := errors.New("boom")
	audits := 0
	g := authn.NewMemberGate(fakeRoles{err: boom}, authn.WithAudit(func(context.Context, uuid.UUID, uuid.UUID) { audits++ }))
	assert.ErrorIs(t, g.RequireMemberOf(context.Background(), who.ID, who.TenantID), boom)
	assert.Zero(t, audits)
}

func TestOwnerGateKeepsPackageRefusal(t *testing.T) {
	mine := errors.New("pkg: not owner")
	who := &authn.User{ID: uuid.New(), TenantID: uuid.New()}
	ctx := context.Background()
	assert.ErrorIs(t, authn.NewOwnerGate(fakeRoles{}, authn.WithRefusal(mine)).RequireOwner(ctx, who), mine)
	assert.ErrorIs(t, authn.NewOwnerGate(nil, authn.WithRefusal(mine)).RequireOwner(ctx, who), mine)
	assert.ErrorIs(t, authn.NewMemberGate(fakeRoles{}, authn.WithRefusal(mine)).RequireMemberOf(ctx, who.ID, who.TenantID), mine)
}

func TestOwnerGateIsOwner(t *testing.T) {
	who := &authn.User{ID: uuid.New(), TenantID: uuid.New()}
	ctx := context.Background()
	ok, err := authn.NewOwnerGate(fakeRoles{owner: true}).IsOwner(ctx, who)
	assert.NoError(t, err)
	assert.True(t, ok)
	ok, _ = authn.NewOwnerGate(fakeRoles{owner: true}).IsOwner(ctx, nil)
	assert.False(t, ok)
}
