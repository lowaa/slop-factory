package agent_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
)

func TestStripUserinfo(t *testing.T) {
	for remote, want := range map[string]string{
		"https://github.com/acme/app":                       "github.com/acme/app",
		"https://user:ghp_secret@github.com/acme/app.git":   "github.com/acme/app.git",
		"https://ghp_secret@github.com/acme/app":            "github.com/acme/app",
		"https://user:p@ss@github.com/acme/app":             "github.com/acme/app",
		"https://user:pa/ss@github.com/acme/app":            "github.com/acme/app",
		"https://user:pa?ss#x@github.com/acme/app":          "github.com/acme/app",
		"ssh://git:secret@github.com:22/acme/app":           "github.com:22/acme/app",
		"git@github.com:acme/app.git":                       "github.com:acme/app.git",
		"https://github.com/acme/app?token=ghp_secret#frag": "github.com/acme/app",
		"  https://github.com/acme/app  ":                   "github.com/acme/app",
		"/srv/mirrors/app.git":                              "/srv/mirrors/app.git",
		"file:///srv/mirrors/app.git":                       "/srv/mirrors/app.git",
		"":                                                  "",
	} {
		assert.Equal(t, want, agent.StripUserinfo(remote), remote)
	}
}

func parse(t *testing.T, layout any) *agent.Layout {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"harness": "claude", "layout": layout})
	require.NoError(t, err)
	return agent.ParseLayout(raw)
}

func TestParseLayout_StripsCredentialsEvenFromAScriptThatDidNot(t *testing.T) {
	layout := parse(t, map[string]any{
		"root":  map[string]any{"path": ".", "git": true, "origin": "https://u:tok@github.com/acme/root"},
		"repos": []any{map[string]any{"path": "app", "git": true, "origin": "https://tok@github.com/acme/app"}},
	})
	require.NotNil(t, layout)
	assert.Equal(t, agent.LayoutEntry{Path: ".", Git: true, Origin: "github.com/acme/root"}, layout.Root)
	assert.Equal(t, []agent.LayoutEntry{{Path: "app", Git: true, Origin: "github.com/acme/app"}}, layout.Repos)
}

func TestParseLayout_OnlyTheRootsDirectChildrenAreKept(t *testing.T) {
	repo := func(path string) map[string]any { return map[string]any{"path": path, "git": true, "origin": "h/x"} }
	layout := parse(t, map[string]any{
		"root":  map[string]any{"path": ".", "git": false},
		"repos": []any{repo("app"), repo("app"), repo("../etc"), repo("a/b"), repo(`a\b`), repo(".."), repo("."), repo(""), repo("ok\x1b[31m")},
	})
	require.NotNil(t, layout)
	assert.Equal(t, []agent.LayoutEntry{{Path: "app", Git: true, Origin: "h/x"}, {Path: "ok[31m", Git: true, Origin: "h/x"}}, layout.Repos)
}

func TestParseLayout_ACapAppliesToRepos(t *testing.T) {
	repos := []any{}
	for i := 0; i < agent.MaxLayoutRepos+10; i++ {
		repos = append(repos, map[string]any{"path": uuid.NewString(), "git": true})
	}
	layout := parse(t, map[string]any{"root": map[string]any{"path": ".", "git": false}, "repos": repos})
	require.NotNil(t, layout)
	assert.Len(t, layout.Repos, agent.MaxLayoutRepos)
}

// Anything it cannot use is no report at all, never an error: the report
// rides on a hook whose answer must not depend on it.
func TestParseLayout_NothingUsableIsNil(t *testing.T) {
	assert.Nil(t, agent.ParseLayout(nil))
	assert.Nil(t, agent.ParseLayout(json.RawMessage(`not json`)))
	assert.Nil(t, agent.ParseLayout(json.RawMessage(`"layout"`)))
	assert.Nil(t, agent.ParseLayout(json.RawMessage(`{"harness":"pi"}`)), "a workspace that has not updated sends no layout")
	assert.Nil(t, parse(t, "nope"))
	assert.Nil(t, parse(t, map[string]any{"repos": []any{}}), "no root")
	assert.Nil(t, parse(t, map[string]any{"root": map[string]any{"path": "elsewhere"}}), "the root is always \".\"")
}

func TestLayoutStore_KeepsTheLatestReportPerAgentAndPerTenant(t *testing.T) {
	database := dbtest.WithDB(t)
	a := db.WithTenant(context.Background(), dbtest.NewTenant(t, database))
	b := db.WithTenant(context.Background(), dbtest.NewTenant(t, database))
	agents := agent.NewService(agent.NewPostgresStore(database), &fakeReleaser{}, nil, slog.DiscardHandler)
	first, err := agents.Create(a, "first")
	require.NoError(t, err)
	second, err := agents.Create(a, "second")
	require.NoError(t, err)
	store := agent.NewPostgresLayoutStore(database)

	old := &agent.Layout{Root: agent.LayoutEntry{Path: ".", Git: true, Origin: "h/old"}, Repos: []agent.LayoutEntry{}}
	latest := &agent.Layout{
		Root:  agent.LayoutEntry{Path: ".", Git: false},
		Repos: []agent.LayoutEntry{{Path: "app", Git: true, Origin: "h/app"}},
	}
	require.NoError(t, store.SaveLayout(a, first.ID, old))
	require.NoError(t, store.SaveLayout(a, first.ID, latest))
	require.NoError(t, store.SaveLayout(a, second.ID, old))

	got, err := store.Layouts(a)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, *latest, got[first.ID].Layout, "a report replaces the one before")
	assert.Equal(t, *old, got[second.ID].Layout)
	assert.WithinDuration(t, time.Now(), got[first.ID].ReportedAt, time.Minute)

	other, err := store.Layouts(b)
	require.NoError(t, err)
	assert.Empty(t, other, "another tenant's reports are hidden")
}
