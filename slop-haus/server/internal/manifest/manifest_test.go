package manifest_test

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/manifest"
)

func TestNormaliseRemote_TheSameRepoReachedDifferentWaysIsOne(t *testing.T) {
	want := "github.com/acme/app"
	for _, remote := range []string{
		"github.com/acme/app",
		"https://github.com/acme/app",
		"https://github.com/acme/app.git",
		"https://github.com/acme/app/",
		"git@github.com:acme/app.git",
		"git@github.com:acme/app",
		"ssh://git@github.com/acme/app.git",
		"ssh://git@github.com:22/acme/app.git",
		"https://user:ghp_secret@github.com/acme/app",
		"HTTPS://GitHub.COM/Acme/App.git",
		"  https://github.com/acme/app  ",
		"github.com:acme/app.git",
		// What the workspace reports once credentials and scheme are gone.
		"github.com:22/acme/app.git",
	} {
		assert.Equal(t, want, manifest.NormaliseRemote(remote), remote)
	}
}

func TestNormaliseRemote_DifferentReposStayDifferent(t *testing.T) {
	assert.NotEqual(t, manifest.NormaliseRemote("github.com/acme/app"), manifest.NormaliseRemote("github.com/acme/app2"))
	assert.NotEqual(t, manifest.NormaliseRemote("github.com/acme/app"), manifest.NormaliseRemote("gitlab.com/acme/app"))
	assert.NotEqual(t, manifest.NormaliseRemote("github.com/acme/app"), manifest.NormaliseRemote("github.com/other/app"))
}

func TestNormaliseRemote_NeverKeepsACredential(t *testing.T) {
	got := manifest.NormaliseRemote("https://user:ghp_secret@github.com/acme/app")
	assert.NotContains(t, got, "ghp_secret")
	assert.NotContains(t, got, "user")
}

func TestNormalise_AcceptsSingleDirectoryPaths(t *testing.T) {
	got, err := manifest.Normalise([]manifest.Repo{
		{Path: " app ", Remote: "git@github.com:Acme/App.git"},
		{Path: "deploy", Remote: "https://github.com/acme/deploy"},
	})
	require.NoError(t, err)
	assert.Equal(t, []manifest.Repo{
		{Path: "app", Remote: "github.com/acme/app"},
		{Path: "deploy", Remote: "github.com/acme/deploy"},
	}, got)
}

func TestNormalise_RefusesWhatCouldNeverMatchOrIsUnsafe(t *testing.T) {
	ok := manifest.Repo{Path: "app", Remote: "github.com/acme/app"}
	tooMany := make([]manifest.Repo, manifest.MaxRepos+1)
	for i := range tooMany {
		tooMany[i] = manifest.Repo{Path: strings.Repeat("d", i+1), Remote: "github.com/acme/app"}
	}
	for name, repos := range map[string][]manifest.Repo{
		"empty":             {},
		"too many":          tooMany,
		"empty path":        {{Path: "", Remote: ok.Remote}},
		"parent":            {{Path: "..", Remote: ok.Remote}},
		"nested":            {{Path: "a/b", Remote: ok.Remote}},
		"backslash":         {{Path: `a\b`, Remote: ok.Remote}},
		"absolute":          {{Path: "/etc", Remote: ok.Remote}},
		"control":           {{Path: "a\x1bb", Remote: ok.Remote}},
		"bidi override":     {{Path: "a‮b", Remote: ok.Remote}},
		"duplicate path":    {ok, {Path: "app", Remote: "github.com/acme/other"}},
		"path with space":   {{Path: "my app", Remote: ok.Remote}},
		"leading dash":      {{Path: "-app", Remote: ok.Remote}},
		"dot git":           {{Path: ".GIT", Remote: ok.Remote}},
		"unicode path":      {{Path: "café", Remote: ok.Remote}},
		"dotdot remote":     {{Path: "app", Remote: "github.com/acme/../x"}},
		"dash host":         {{Path: "app", Remote: "-oProxyCommand=x/a/b"}},
		"remote metachars":  {{Path: "app", Remote: "github.com/acme/a;b"}},
		"no remote":         {{Path: "app", Remote: " "}},
		"not a repository":  {{Path: "app", Remote: "github.com"}},
		"remote with space": {{Path: "app", Remote: "github.com/acme/my app"}},
		"remote too long":   {{Path: "app", Remote: "github.com/" + strings.Repeat("a", manifest.MaxRemote)}},
	} {
		_, err := manifest.Normalise(repos)
		assert.ErrorIs(t, err, manifest.ErrInvalid, name)
	}
}

func TestRepoFor_TheRepoContainingTheScope(t *testing.T) {
	repos := []manifest.Repo{{Path: "docs", Remote: "r/docs"}, {Path: "app", Remote: "r/app"}}
	for scope, want := range map[string]string{
		"app/src/":   "app",
		"app":        "app",
		"./app/x.go": "app",
		"docs/":      "docs",
	} {
		got := manifest.RepoFor(repos, scope)
		require.NotNil(t, got, scope)
		assert.Equal(t, want, got.Path, scope)
	}
	for _, scope := range []string{"application", "main.go", "docs/"} {
		assert.Nil(t, manifest.RepoFor([]manifest.Repo{{Path: "app", Remote: "r/app"}}, scope), "a scope in no repo belongs to none: "+scope)
	}
}

func layout(rootGit bool, rootOrigin string, repos ...agent.LayoutEntry) *agent.Layout {
	if repos == nil {
		repos = []agent.LayoutEntry{}
	}
	return &agent.Layout{Root: agent.LayoutEntry{Path: ".", Git: rootGit, Origin: rootOrigin}, Repos: repos}
}

func repoEntry(path, origin string) agent.LayoutEntry {
	return agent.LayoutEntry{Path: path, Git: true, Origin: origin}
}

func TestCheck(t *testing.T) {
	declared := []manifest.Repo{{Path: "app", Remote: "github.com/acme/app"}, {Path: "deploy", Remote: "github.com/acme/deploy"}}

	t.Run("a match compares remotes after normalising", func(t *testing.T) {
		l := layout(false, "", repoEntry("app", "github.com:acme/app.git"), repoEntry("deploy", "github.com/Acme/deploy"))
		assert.Empty(t, manifest.Check(declared, l))
	})
	t.Run("directories the manifest does not list are ignored", func(t *testing.T) {
		l := layout(false, "", repoEntry("app", "github.com/acme/app"),
			repoEntry("deploy", "github.com/acme/deploy"), repoEntry("scratch", "github.com/me/scratch"))
		assert.Empty(t, manifest.Check(declared, l))
	})
	t.Run("a repo that is missing", func(t *testing.T) {
		got := manifest.Check(declared, layout(false, "", repoEntry("app", "github.com/acme/app")))
		assert.Equal(t, []manifest.Mismatch{{Path: "deploy", Problem: manifest.ProblemNoRepo, Expected: "github.com/acme/deploy"}}, got)
	})
	t.Run("a directory that is not a git repo", func(t *testing.T) {
		l := layout(false, "", repoEntry("app", "github.com/acme/app"), agent.LayoutEntry{Path: "deploy", Git: false})
		got := manifest.Check(declared, l)
		require.Len(t, got, 1)
		assert.Equal(t, manifest.ProblemNoRepo, got[0].Problem)
	})
	t.Run("a repo with no origin", func(t *testing.T) {
		l := layout(false, "", repoEntry("app", ""), repoEntry("deploy", "github.com/acme/deploy"))
		got := manifest.Check(declared, l)
		require.Len(t, got, 1)
		assert.Equal(t, manifest.Mismatch{Path: "app", Problem: manifest.ProblemNoOrigin, Expected: "github.com/acme/app"}, got[0])
	})
	t.Run("the wrong repo says what was found", func(t *testing.T) {
		l := layout(false, "", repoEntry("app", "github.com/acme/fork"), repoEntry("deploy", "github.com/acme/deploy"))
		got := manifest.Check(declared, l)
		require.Len(t, got, 1)
		assert.Equal(t, manifest.Mismatch{Path: "app", Problem: manifest.ProblemWrongOrigin,
			Expected: "github.com/acme/app", Found: "github.com/acme/fork"}, got[0])
		assert.Contains(t, got[0].Describe(), "github.com/acme/fork")
	})
	t.Run("a root repo with no origin is a mismatch too, beside the manifest's own", func(t *testing.T) {
		l := layout(true, "", repoEntry("app", "github.com/acme/app"))
		got := manifest.Check(declared, l)
		require.Len(t, got, 2)
		assert.Equal(t, manifest.ProblemRootIsRepo, got[0].Problem)
		assert.Equal(t, manifest.Mismatch{Path: "deploy", Problem: manifest.ProblemNoRepo, Expected: "github.com/acme/deploy"}, got[1])
	})
	t.Run("a root that is a repo is a mismatch", func(t *testing.T) {
		l := layout(true, "github.com/acme/mono", repoEntry("app", "github.com/acme/app"), repoEntry("deploy", "github.com/acme/deploy"))
		got := manifest.Check(declared, l)
		require.Len(t, got, 1)
		assert.Equal(t, manifest.ProblemRootIsRepo, got[0].Problem)
		assert.Equal(t, ".", got[0].Path)
		assert.Contains(t, got[0].Describe(), "the workspace root must not be a repo")
	})
}

func report(name string, l *agent.Layout) manifest.AgentLayout {
	return manifest.AgentLayout{AgentID: uuid.New(), AgentName: name, Layout: *l}
}

func names(ds []manifest.Disagreement) []string {
	out := []string{}
	for _, d := range ds {
		out = append(out, d.AgentName)
	}
	return out
}

func TestPropose(t *testing.T) {
	root := func() *agent.Layout {
		return layout(false, "", repoEntry("mono", "https://user:tok@github.com/acme/mono.git"))
	}

	t.Run("a single repo workspace proposes its folder", func(t *testing.T) {
		p := manifest.Propose([]manifest.AgentLayout{report("a", root()), report("b", root())})
		assert.Equal(t, []manifest.Repo{{Path: "mono", Remote: "github.com/acme/mono"}}, p.Repos)
		assert.Empty(t, p.Reason)
		assert.Empty(t, p.Disagreements)
	})
	t.Run("a root that is a repo is never proposed, only its subfolder repos", func(t *testing.T) {
		l := func() *agent.Layout {
			return layout(true, "github.com/acme/mono", repoEntry("app", "github.com/acme/app"))
		}
		p := manifest.Propose([]manifest.AgentLayout{report("a", l()), report("b", l())})
		assert.Equal(t, []manifest.Repo{{Path: "app", Remote: "github.com/acme/app"}}, p.Repos)
		onlyRoot := manifest.Propose([]manifest.AgentLayout{report("a", layout(true, "github.com/acme/mono"))})
		assert.Empty(t, onlyRoot.Repos)
		assert.Equal(t, manifest.ReasonNoRepos, onlyRoot.Reason)
	})
	t.Run("a workspace proposes its top-level repos, sorted", func(t *testing.T) {
		l := func() *agent.Layout {
			return layout(false, "", repoEntry("deploy", "github.com/acme/deploy"), repoEntry("app", "github.com/acme/app"),
				agent.LayoutEntry{Path: "notes", Git: false}, repoEntry("local", ""))
		}
		p := manifest.Propose([]manifest.AgentLayout{report("a", l())})
		assert.Equal(t, []manifest.Repo{
			{Path: "app", Remote: "github.com/acme/app"}, {Path: "deploy", Remote: "github.com/acme/deploy"},
		}, p.Repos, "a non-repo and a repo with no origin are not proposed")
	})
	t.Run("the agent that disagrees with the rest is named, not obeyed", func(t *testing.T) {
		other := layout(false, "", repoEntry("fork", "github.com/acme/fork"))
		p := manifest.Propose([]manifest.AgentLayout{report("a", root()), report("b", root()), report("odd", other)})
		assert.Equal(t, []manifest.Repo{{Path: "mono", Remote: "github.com/acme/mono"}}, p.Repos)
		assert.Equal(t, []string{"odd"}, names(p.Disagreements))
		assert.Equal(t, []manifest.Repo{{Path: "fork", Remote: "github.com/acme/fork"}}, p.Disagreements[0].Found)
	})
	t.Run("the most common layout wins even when its signature sorts after a minority's", func(t *testing.T) {
		early := func() *agent.Layout { return layout(false, "", repoEntry("aaa", "github.com/acme/aaa")) }
		late := func() *agent.Layout { return layout(false, "", repoEntry("zzz", "github.com/acme/zzz")) }
		p := manifest.Propose([]manifest.AgentLayout{report("lone", early()), report("a", late()), report("b", late())})
		assert.Equal(t, []manifest.Repo{{Path: "zzz", Remote: "github.com/acme/zzz"}}, p.Repos)
		assert.Equal(t, []string{"lone"}, names(p.Disagreements))
	})
	t.Run("an agent with no repo at all is the odd one out too", func(t *testing.T) {
		p := manifest.Propose([]manifest.AgentLayout{report("a", root()), report("b", root()), report("bare", layout(false, ""))})
		assert.NotEmpty(t, p.Repos)
		assert.Equal(t, []string{"bare"}, names(p.Disagreements))
		assert.Empty(t, p.Disagreements[0].Found)
	})
	t.Run("two agents that differ get no proposal: nobody is picked", func(t *testing.T) {
		p := manifest.Propose([]manifest.AgentLayout{report("a", root()), report("b", layout(false, "", repoEntry("fork", "github.com/acme/fork")))})
		assert.Empty(t, p.Repos)
		assert.Equal(t, manifest.ReasonNoConsensus, p.Reason)
		assert.Equal(t, []string{"a", "b"}, names(p.Disagreements), "both are named")
	})
	t.Run("a tie at the top gets no proposal", func(t *testing.T) {
		fork := func() *agent.Layout { return layout(false, "", repoEntry("fork", "github.com/acme/fork")) }
		p := manifest.Propose([]manifest.AgentLayout{report("a", root()), report("b", root()), report("c", fork()), report("d", fork())})
		assert.Empty(t, p.Repos)
		assert.Equal(t, manifest.ReasonNoConsensus, p.Reason)
		assert.Len(t, p.Disagreements, 4)
	})
	t.Run("nobody has reported", func(t *testing.T) {
		p := manifest.Propose(nil)
		assert.Empty(t, p.Repos)
		assert.Equal(t, manifest.ReasonNoReports, p.Reason)
	})
	t.Run("agents that agree there is no repo get no proposal", func(t *testing.T) {
		p := manifest.Propose([]manifest.AgentLayout{report("a", layout(false, "")), report("b", layout(false, ""))})
		assert.Empty(t, p.Repos)
		assert.Equal(t, manifest.ReasonNoRepos, p.Reason)
	})
}
