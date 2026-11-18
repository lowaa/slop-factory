package change

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/scope"
)

func file(p string) codehost.FileDiff {
	return codehost.FileDiff{Path: p, Status: codehost.DiffModified}
}

// A repo checked out below the workspace root: the ticket's paths are
// workspace-relative, the drift's and the manifest's are repo-relative.
func TestDecideDrift_NestedRepoComparesTicketPathsInWorkspaceTerms(t *testing.T) {
	rule := &driftRule{RepoPath: "mono", Apps: []string{"a/", "b/"}, TicketPaths: []string{"mono/a/server/"},
		Shared: []scope.SharedPathRule{{Path: "b/migrations/"}}}

	assert.Equal(t, driftWouldAccept, decideDrift(rule, []codehost.FileDiff{file("b/x.go")}, nil).Decision)
	assert.Equal(t, driftWouldRefuse, decideDrift(rule, []codehost.FileDiff{file("a/server/x.go")}, nil).Decision, "the ticket's own path, which is mono/a/server/")
	assert.Equal(t, driftWouldRefuse, decideDrift(rule, []codehost.FileDiff{file("b/migrations/1.sql")}, nil).Decision, "a shared path of the repo")
}

func TestDecideDrift_NothingChangedVouchesForNothing(t *testing.T) {
	rule := &driftRule{RepoPath: "repo", Apps: []string{"a/"}}

	d := decideDrift(rule, nil, nil)

	assert.Equal(t, driftWouldRefuse, d.Decision)
	assert.Contains(t, d.Reason, "no file in an independent app")
}

func TestDecideDrift_TheTicketsOwnAppIsRefused(t *testing.T) {
	apps := []string{"a/", "b/", "c/"}
	for name, tc := range map[string]struct {
		paths  []string
		drift  string
		accept bool
		why    string
		nested bool
	}{
		"same app, outside the ticket's path": {paths: []string{"mono/a/server/"}, drift: "a/web/y.go",
			why: "a/web/y.go is in this ticket's own app a/"},
		"another app": {paths: []string{"mono/a/server/"}, drift: "b/x.go", accept: true},
		"two apps, the first": {paths: []string{"mono/a/server/", "mono/b/web/"}, drift: "a/web/y.go",
			why: "a/web/y.go is in this ticket's own app a/"},
		"two apps, the second": {paths: []string{"mono/a/server/", "mono/b/web/"}, drift: "b/server/y.go",
			why: "b/server/y.go is in this ticket's own app b/"},
		"two apps, a third":     {paths: []string{"mono/a/server/", "mono/b/web/"}, drift: "c/x.go", accept: true},
		"nested apps":           {paths: []string{"mono/a/b/x/"}, drift: "a/b/y.go", why: "a/b/y.go is in this ticket's own app a/b/", nested: true},
		"a library adds no app": {paths: []string{"mono/go-db/"}, drift: "a/x.go", accept: true},
		"an app directory without its slash is read as the app, to be safe": {paths: []string{"mono/a"}, drift: "a/x.go",
			why: "a/x.go is in this ticket's own app a/"},
	} {
		t.Run(name, func(t *testing.T) {
			rule := &driftRule{RepoPath: "mono", Apps: apps, TicketPaths: tc.paths}
			if tc.nested {
				rule.Apps = []string{"a/b/", "a/"}
			}

			d := decideDrift(rule, []codehost.FileDiff{file(tc.drift)}, nil)

			if tc.accept {
				assert.Equal(t, driftWouldAccept, d.Decision, d.Reason)
				return
			}
			assert.Equal(t, driftWouldRefuse, d.Decision)
			assert.Contains(t, d.Reason, tc.why)
		})
	}
}

func TestDecideDrift_FirstReasonWinsOverTheOwnAppReason(t *testing.T) {
	rule := &driftRule{RepoPath: "mono", Apps: []string{"a/"}, TicketPaths: []string{"mono/a/server/"}}

	d := decideDrift(rule, []codehost.FileDiff{file("go.work"), file("a/web/y.go")}, nil)

	assert.Equal(t, "go.work is not in an independent app", d.Reason)
}
