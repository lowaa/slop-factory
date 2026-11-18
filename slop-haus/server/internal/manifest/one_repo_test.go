package manifest_test

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/codehosttest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/manifest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// The one-repo rules run through the real ticket service against the real
// manifest store, since where a scope falls is decided by what is stored.

type oneRepo struct {
	*tenantFixture
	tix *ticket.Service
	// store seeds manifests that still have a single repo, which Set now refuses.
	store manifest.Store
}

func (e *env) enforcing() *oneRepo {
	f := e.tenant()
	return &oneRepo{
		tenantFixture: f,
		store:         manifest.NewPostgresStore(e.database),
		tix: ticket.NewService(ticket.NewPostgresStore(e.database, ticket.WithStoreWorkspace(manifest.NewPolicy(manifest.NewPostgresStore(e.database)))), slog.DiscardHandler,
			ticket.WithWorkspace(manifest.NewPolicy(manifest.NewPostgresStore(e.database))),
			ticket.WithCodeHosts(codehosttest.Registry(codehosttest.NewLinkHost("github.com"), codehosttest.NewLinkHost("gitea.example")))),
	}
}

func (f *oneRepo) declare(repos ...manifest.Repo) {
	f.t.Helper()
	clean, err := manifest.Normalise(repos)
	require.NoError(f.t, err)
	_, err = f.store.Put(f.ctx, clean, f.owner.ID)
	require.NoError(f.t, err)
}

func (f *oneRepo) file(paths ...string) (*ticket.Ticket, error) {
	ps := make([]model.Path, len(paths))
	for i, p := range paths {
		ps[i] = model.Path{Path: p}
	}
	return f.tix.Create(f.ctx, &ticket.CreateParams{Title: "t", Paths: ps, Status: ticket.StatusOpen})
}

func (f *oneRepo) mustFile(paths ...string) *ticket.Ticket {
	f.t.Helper()
	tk, err := f.file(paths...)
	require.NoError(f.t, err)
	return tk
}

func TestScopes_NoManifestEnforcesNothing(t *testing.T) {
	f := newEnv(t).enforcing()
	tk, err := f.file("app/src/", "deploy/k8s/", "README.md")
	require.NoError(t, err, "a tenant with no manifest keeps today's behaviour")

	_, err = f.tix.Update(f.ctx, tk.ID, &ticket.UpdateParams{Paths: []model.Path{{Path: "a/"}, {Path: "b/"}}})
	require.NoError(t, err)
}

func TestScopes_SpanningReposIsRefusedOnCreateAndUpdate(t *testing.T) {
	f := newEnv(t).enforcing()
	f.declare(manifest.Repo{Path: "app", Remote: "github.com/acme/app"}, manifest.Repo{Path: "deploy", Remote: "github.com/acme/deploy"})

	_, err := f.file("app/src/", "deploy/k8s/")
	require.ErrorIs(t, err, ticket.ErrScopesSpanRepos)
	assert.Contains(t, err.Error(), "github.com/acme/app")
	assert.Contains(t, err.Error(), "github.com/acme/deploy")
	assert.Contains(t, err.Error(), "ticket_blocker_add", "tells the agent to split the work into linked tickets")

	tk := f.mustFile("app/src/", "app/go.mod")
	_, err = f.tix.Update(f.ctx, tk.ID, &ticket.UpdateParams{Paths: []model.Path{{Path: "app/"}, {Path: "deploy/"}}})
	require.ErrorIs(t, err, ticket.ErrScopesSpanRepos)
	got, err := f.tix.Get(f.ctx, tk.ID)
	require.NoError(t, err)
	assert.Len(t, got.Paths, 2, "a refused edit changes nothing")

	_, err = f.tix.Update(f.ctx, tk.ID, &ticket.UpdateParams{Paths: []model.Path{{Path: "app/other/"}}})
	require.NoError(t, err, "still one repo")
}

func TestScopes_OutsideEveryRepoIsRefused(t *testing.T) {
	f := newEnv(t).enforcing()
	f.declare(manifest.Repo{Path: "app", Remote: "github.com/acme/app"})
	_, err := f.file("app/src/", "README.md")
	require.ErrorIs(t, err, ticket.ErrScopesSpanRepos)
	assert.Contains(t, err.Error(), "README.md")
	// A directory name that merely starts with the repo's path is not in it.
	_, err = f.file("application/x.go")
	require.ErrorIs(t, err, ticket.ErrScopesSpanRepos)
}

func TestScopes_ExistingTicketsAreNotRevalidatedOnAdoption(t *testing.T) {
	f := newEnv(t).enforcing()
	tk := f.mustFile("app/src/", "deploy/k8s/")
	f.declare(manifest.Repo{Path: "app", Remote: "github.com/acme/app"}, manifest.Repo{Path: "deploy", Remote: "github.com/acme/deploy"})

	got, err := f.tix.Get(f.ctx, tk.ID)
	require.NoError(t, err)
	assert.Len(t, got.Paths, 2, "adopting a manifest leaves open tickets as they are")
	_, err = f.tix.Update(f.ctx, tk.ID, &ticket.UpdateParams{Priority: ptr(model.PriorityHigh)})
	require.NoError(t, err, "an edit that leaves scopes alone is not held to the rule")
}

func TestScopes_ExtendingMustStayInTheTicketsRepo(t *testing.T) {
	f := newEnv(t).enforcing()
	f.declare(manifest.Repo{Path: "app", Remote: "github.com/acme/app"}, manifest.Repo{Path: "deploy", Remote: "github.com/acme/deploy"})
	tk := f.mustFile("app/src/")
	a := f.newAgent("worker", nil)
	claimed, err := f.tix.Claim(f.ctx, a.ID, &tk.ID)
	require.NoError(t, err)
	require.NotNil(t, claimed.Ticket)

	_, err = f.tix.ExtendScope(f.ctx, a.ID, []model.Path{{Path: "deploy/k8s/"}})
	require.ErrorIs(t, err, ticket.ErrScopesSpanRepos)

	res, err := f.tix.ExtendScope(f.ctx, a.ID, []model.Path{{Path: "app/docs/"}})
	require.NoError(t, err)
	assert.True(t, res.Granted)
}

func TestDone_PRMustBeInTheTicketsRepo(t *testing.T) {
	const (
		ok    = "https://github.com/acme/app/pull/7"
		other = "https://github.com/acme/deploy/pull/7"
	)
	for _, tc := range []struct {
		name     string
		repos    []manifest.Repo
		paths    []string
		pr       string
		refused  bool
		contains string
	}{
		{name: "the repo's own PR", repos: twoRepos, paths: []string{"app/x/"}, pr: ok},
		{name: "case and URL form differ", repos: twoRepos, paths: []string{"app/x/"}, pr: "https://github.com/Acme/APP/pull/7/files"},
		{name: "another repo's PR", repos: twoRepos, paths: []string{"app/x/"}, pr: other, refused: true, contains: "github.com/acme/deploy"},
		{name: "a different owner", repos: twoRepos, paths: []string{"app/x/"}, pr: "https://github.com/evil/app/pull/7", refused: true},
		{name: "a single repo", repos: []manifest.Repo{{Path: "app", Remote: "git@github.com:acme/app.git"}}, paths: []string{"app/x/"}, pr: ok},
		{name: "a single repo, wrong PR", repos: []manifest.Repo{{Path: "app", Remote: "git@github.com:acme/app.git"}}, paths: []string{"app/x/"}, pr: other, refused: true},
		{name: "a github link that is no PR", repos: twoRepos, paths: []string{"app/x/"}, pr: "https://github.com/acme/app/issues/7", refused: true},
		{name: "a PR on another host", repos: twoRepos, paths: []string{"app/x/"}, pr: "https://gitlab.com/acme/app/-/merge_requests/7", refused: true},
		{name: "no PR reported", repos: twoRepos, paths: []string{"app/x/"}, pr: ""},
		{name: "a repo on another forge, same host", repos: []manifest.Repo{{Path: "app", Remote: "git.example.com/acme/app"}},
			paths: []string{"app/x/"}, pr: "https://git.example.com/acme/app/-/merge_requests/7"},
		{name: "a repo on another forge, a different repo's link", repos: []manifest.Repo{{Path: "app", Remote: "git.example.com/acme/app"}},
			paths: []string{"app/x/"}, pr: "https://git.example.com/acme/other/-/merge_requests/7", refused: true},
		{name: "a repo on another forge, a prefix-sharing repo", repos: []manifest.Repo{{Path: "app", Remote: "git.example.com/acme/app"}},
			paths: []string{"app/x/"}, pr: "https://git.example.com/acme/app2/pulls/7", refused: true},
		{name: "a repo on another forge, GitHub PR", repos: []manifest.Repo{{Path: "app", Remote: "git.example.com/acme/app"}},
			paths: []string{"app/x/"}, pr: ok, refused: true},
		{name: "a registered host, its own PR", repos: []manifest.Repo{{Path: "app", Remote: "gitea.example/acme/app"}},
			paths: []string{"app/x/"}, pr: "https://gitea.example/Acme/App/pull/7/files"},
		{name: "a registered host, another repo's PR", repos: []manifest.Repo{{Path: "app", Remote: "gitea.example/acme/app"}},
			paths: []string{"app/x/"}, pr: "https://gitea.example/acme/other/pull/7", refused: true, contains: "gitea.example/acme/other"},
		{name: "a registered host, a link that is no PR", repos: []manifest.Repo{{Path: "app", Remote: "gitea.example/acme/app"}},
			paths: []string{"app/x/"}, pr: "https://gitea.example/acme/app/issues/7", refused: true},
		{name: "a registered host's repo, the same path on GitHub", repos: []manifest.Repo{{Path: "app", Remote: "gitea.example/acme/app"}},
			paths: []string{"app/x/"}, pr: ok, refused: true},
		{name: "no manifest", repos: nil, paths: []string{"app/x/"}, pr: other},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newEnv(t).enforcing()
			if tc.repos != nil {
				f.declare(tc.repos...)
			}
			tk := f.mustFile(tc.paths...)
			a := f.newAgent("worker", nil)
			_, err := f.tix.Claim(f.ctx, a.ID, &tk.ID)
			require.NoError(t, err)

			_, err = f.tix.Done(f.ctx, a.ID, &ticket.DoneParams{Summary: "done", PRURL: tc.pr})
			if !tc.refused {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, ticket.ErrPRWrongRepo)
			assert.Contains(t, err.Error(), tc.contains)
			got, gerr := f.tix.Get(f.ctx, tk.ID)
			require.NoError(t, gerr)
			assert.Equal(t, ticket.StatusInProgress, got.Status, "a refused done leaves the ticket held, to retry")
		})
	}
}

func TestDone_ATicketFiledBeforeAdoptionThatSpansReposIsNotChecked(t *testing.T) {
	f := newEnv(t).enforcing()
	tk := f.mustFile("app/x/", "deploy/y/")
	a := f.newAgent("worker", nil)
	_, err := f.tix.Claim(f.ctx, a.ID, &tk.ID)
	require.NoError(t, err)
	f.declare(twoRepos...)

	_, err = f.tix.Done(f.ctx, a.ID, &ticket.DoneParams{Summary: "done", PRURL: "https://github.com/anyone/anything/pull/1"})
	require.NoError(t, err, "no single repo to check against")
}

func ptr[T any](v T) *T { return &v }

func TestReview_PRMustBeInTheTicketsRepo(t *testing.T) {
	f := newEnv(t).enforcing()
	f.declare(twoRepos...)
	tk := f.mustFile("app/x/")
	a := f.newAgent("worker", nil)
	_, err := f.tix.Claim(f.ctx, a.ID, &tk.ID)
	require.NoError(t, err)

	_, err = f.tix.Review(f.ctx, a.ID, &ticket.ReviewParams{Reason: "reviewed by a person", PRURL: "https://github.com/acme/deploy/pull/1", Handoff: "h"})
	require.ErrorIs(t, err, ticket.ErrPRWrongRepo)
	_, err = f.tix.Review(f.ctx, a.ID, &ticket.ReviewParams{Reason: "reviewed by a person", PRURL: "https://github.com/acme/app/pull/1", Handoff: "h"})
	require.NoError(t, err)
}
