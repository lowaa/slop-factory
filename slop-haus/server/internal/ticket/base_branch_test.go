package ticket_test

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/codehosttest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/scope"
)

func resolveWith(t *testing.T, host *codehosttest.Fake, manifest *scope.WorkspaceRepo) (*ticket.ResolvedBase, error) {
	t.Helper()
	r := ticket.NewBaseResolver("fallback", slog.New(slog.DiscardHandler))
	return r.Resolve(context.Background(), &ticket.BaseQuery{Repo: codehost.Repo{Owner: "o", Name: "r"}, Changes: host, Manifest: manifest})
}

func TestBaseResolver_ChainOrder(t *testing.T) {
	t.Parallel()
	repo := codehost.Repo{Owner: "o", Name: "r"}
	host := codehosttest.NewLinkHost("h")
	host.HeadOf = map[codehost.Repo]*codehost.BranchHead{repo: {Branch: "trunk", SHA: "s"}}

	rb, err := resolveWith(t, host, &scope.WorkspaceRepo{BaseBranch: "tenant"})
	require.NoError(t, err)
	assert.Equal(t, "tenant", rb.Branch, "the manifest comes first")
	assert.True(t, rb.Explicit)
	assert.Empty(t, host.HeadReads(), "no host read when the manifest answers")

	rb, err = resolveWith(t, host, &scope.WorkspaceRepo{})
	require.NoError(t, err)
	assert.Equal(t, "trunk", rb.Branch, "then the repo's default branch")
	assert.False(t, rb.Explicit)
	assert.NotNil(t, rb.Head)

	rb, err = resolveWith(t, codehosttest.NewLinkHost("h"), nil)
	require.NoError(t, err)
	assert.Equal(t, "fallback", rb.Branch, "a host that does not know the repo falls to the fixed branch")
}

func TestBaseResolver_AHostThatCannotAnswerStopsTheChain(t *testing.T) {
	t.Parallel()
	host := codehosttest.NewLinkHost("h")
	host.HeadErr = codehost.ErrUnreachable

	_, err := resolveWith(t, host, &scope.WorkspaceRepo{})

	require.ErrorIs(t, err, codehost.ErrUnreachable)
}

func TestLockNameFor_OnlyAnExplicitBaseHasALockOfItsOwn(t *testing.T) {
	t.Parallel()
	assert.Equal(t, ticket.IntegrationLockName, ticket.LockNameFor("app", &ticket.ResolvedBase{Branch: "main"}))
	assert.Equal(t, ticket.IntegrationLockName, ticket.LockNameFor("app", nil))
	a := ticket.LockNameFor("app", &ticket.ResolvedBase{Branch: "x", Explicit: true})
	assert.NotEqual(t, ticket.IntegrationLockName, a)
	assert.NotEqual(t, a, ticket.LockNameFor("app", &ticket.ResolvedBase{Branch: "y", Explicit: true}))
	assert.NotEqual(t, a, ticket.LockNameFor("web", &ticket.ResolvedBase{Branch: "x", Explicit: true}))
}

func TestBaseOrDefault(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct{ in, want string }{
		"explicit": {"release", "release"},
		"default":  {"", ticket.DefaultBaseBranch},
	} {
		t.Run(name, func(t *testing.T) { assert.Equal(t, tc.want, ticket.BaseOrDefault(tc.in)) })
	}
}

func TestBranchLockName_AgreesWithLockNameForAndParses(t *testing.T) {
	t.Parallel()
	assert.Equal(t, ticket.LockNameFor("app", &ticket.ResolvedBase{Branch: "x", Explicit: true}), ticket.BranchLockName("app", "x"))
	repo, branch := ticket.ParseLockName(ticket.BranchLockName("app", "x"))
	assert.Equal(t, "app", repo)
	assert.Equal(t, "x", branch)
}
