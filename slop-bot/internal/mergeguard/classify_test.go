package mergeguard_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/mergeguard"
)

// fakeGit answers git queries from a table keyed by "dir: args", and
// records each one; anything it has no answer for fails, as git does
// outside a repo.
type fakeGit struct {
	answers map[string]string
	mu      sync.Mutex
	asked   []string
}

func (g *fakeGit) Run(_ context.Context, dir string, args ...string) (string, error) {
	key := dir + ": " + strings.Join(args, " ")
	g.mu.Lock()
	g.asked = append(g.asked, key)
	g.mu.Unlock()
	if out, ok := g.answers[key]; ok {
		return out + "\n", nil
	}
	return "", errors.New("fatal: not a git repository")
}

// onBranch is a repo at /wt on branch, with origin's HEAD at develop and
// the branch's push destination at origin/<push>.
func onBranch(branch, push string) *fakeGit {
	a := map[string]string{
		"/wt: symbolic-ref --quiet --short HEAD":                     branch,
		"/wt: symbolic-ref --quiet --short refs/remotes/origin/HEAD": "origin/develop",
	}
	if push != "" {
		a["/wt: rev-parse --abbrev-ref --symbolic-full-name @{push}"] = "origin/" + push
	}
	return &fakeGit{answers: a}
}

func TestClassifyCommands(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		cmd  string
		// git is the repo the command runs in; nil is feature-1 pushing
		// to origin/feature-1.
		git  *fakeGit
		want mergeguard.Kind // "" for not a merge
	}{
		// gh pr merge, however it's spelt.
		{name: "gh pr merge", cmd: "gh pr merge 12 --squash", want: mergeguard.KindPRMerge},
		{name: "gh pr merge with repo before the subcommand", cmd: "gh pr -R o/r merge 12", want: mergeguard.KindPRMerge},
		{name: "gh pr merge --auto", cmd: "gh pr merge --auto --squash", want: mergeguard.KindPRMerge},
		{name: "gh by path", cmd: "/usr/bin/gh pr merge", want: mergeguard.KindPRMerge},
		{name: "quoted words", cmd: `"gh" 'pr' "merge"`, want: mergeguard.KindPRMerge},
		{name: "escaped letters", cmd: `g\h pr mer\ge`, want: mergeguard.KindPRMerge},
		{name: "after &&", cmd: "go test ./... && gh pr merge", want: mergeguard.KindPRMerge},
		{name: "after ;", cmd: "echo hi; gh pr merge", want: mergeguard.KindPRMerge},
		{name: "in a pipeline", cmd: "yes | gh pr merge", want: mergeguard.KindPRMerge},
		{name: "on a later line", cmd: "echo one\ngh pr merge 3", want: mergeguard.KindPRMerge},
		{name: "in a subshell", cmd: "(cd /tmp && gh pr merge)", want: mergeguard.KindPRMerge},
		{name: "in a command substitution", cmd: `echo "$(gh pr merge 4)"`, want: mergeguard.KindPRMerge},
		{name: "in an if", cmd: "if true; then gh pr merge; fi", want: mergeguard.KindPRMerge},
		{name: "env wrapper", cmd: "env GH_TOKEN=x gh pr merge", want: mergeguard.KindPRMerge},
		{name: "assignment prefix", cmd: "GH_REPO=o/r gh pr merge", want: mergeguard.KindPRMerge},
		{name: "nested wrappers", cmd: "sudo -u me nohup timeout 30s gh pr merge", want: mergeguard.KindPRMerge},
		{name: "xargs", cmd: "echo 5 | xargs -n1 gh pr merge", want: mergeguard.KindPRMerge},
		{name: "bash -c", cmd: `bash -c "gh pr merge 5"`, want: mergeguard.KindPRMerge},
		{name: "bash -lc single-quoted", cmd: `bash -lc 'cd x && gh pr merge'`, want: mergeguard.KindPRMerge},
		{name: "sh -c nested in bash -c", cmd: `bash -c "sh -c 'gh pr merge'"`, want: mergeguard.KindPRMerge},
		{name: "eval", cmd: `eval "gh pr merge 6"`, want: mergeguard.KindPRMerge},
		{name: "env -S", cmd: `env -S "gh pr merge 7"`, want: mergeguard.KindPRMerge},
		{name: "glab mr merge", cmd: "glab mr merge 8", want: mergeguard.KindPRMerge},

		// What isn't a merge.
		{name: "gh pr view", cmd: "gh pr view 12", want: ""},
		{name: "gh pr create mentioning merge", cmd: `gh pr create --title "merge the parser" --body "gh pr merge later"`, want: ""},
		{name: "echo of the words", cmd: `echo "gh pr merge"`, want: ""},
		{name: "grep for it", cmd: `grep -r "gh pr merge" .`, want: ""},
		{name: "a comment", cmd: "ls # gh pr merge", want: ""},
		{name: "git merge locally", cmd: "git merge origin/main", want: ""},
		{name: "command -v", cmd: "command -v gh", want: ""},
		{name: "gh pr checks", cmd: "gh pr checks --watch", want: ""},

		// gh api.
		{name: "gh api REST merge", cmd: "gh api -X PUT repos/o/r/pulls/12/merge -f merge_method=squash", want: mergeguard.KindAPIMerge},
		{name: "gh api REST merge, method attached", cmd: "gh api -XPUT /repos/o/r/pulls/12/merge", want: mergeguard.KindAPIMerge},
		{name: "gh api REST merge, --method=", cmd: "gh api --method=PUT repos/o/r/pulls/12/merge", want: mergeguard.KindAPIMerge},
		{name: "gh api REST merge, method after endpoint", cmd: "gh api repos/{owner}/{repo}/pulls/12/merge --method PUT", want: mergeguard.KindAPIMerge},
		{name: "gh api merge-a-branch", cmd: "gh api repos/o/r/merges -f base=main -f head=x", want: mergeguard.KindAPIMerge},
		{name: "gh api GET of the merge status", cmd: "gh api repos/o/r/pulls/12/merge -X GET", want: ""},
		{name: "gh api pull", cmd: "gh api repos/o/r/pulls/12", want: ""},
		{name: "gh api graphql merge", cmd: `gh api graphql -f query='mutation { mergePullRequest(input:{pullRequestId:"x"}) { clientMutationId } }'`, want: mergeguard.KindAPIMerge},
		{name: "gh api graphql auto-merge", cmd: `gh api graphql -F query='mutation { enablePullRequestAutoMerge(input:{}) { clientMutationId } }'`, want: mergeguard.KindAPIMerge},
		{name: "gh api graphql query", cmd: `gh api graphql -f query='query { viewer { login } }'`, want: ""},
		{name: "gh api graphql --input", cmd: "gh api graphql --input q.json", want: mergeguard.KindAPIMerge},
		{name: "curl to the merge endpoint", cmd: `curl -X PUT -H "Authorization: token $T" https://api.github.com/repos/o/r/pulls/3/merge`, want: mergeguard.KindAPIMerge},

		// git push.
		{name: "push to main", cmd: "git push origin main", want: mergeguard.KindPushDefault},
		{name: "push to master", cmd: "git push origin master", want: mergeguard.KindPushDefault},
		{name: "push to the remote's default", cmd: "git push origin develop", want: mergeguard.KindPushDefault},
		{name: "push HEAD:main", cmd: "git push origin HEAD:main", want: mergeguard.KindPushDefault},
		{name: "push to refs/heads/main", cmd: "git push origin feature-1:refs/heads/main", want: mergeguard.KindPushDefault},
		{name: "force push to main", cmd: "git push -f origin +feature-1:main", want: mergeguard.KindPushDefault},
		{name: "push with options first", cmd: "git push -u --force-with-lease origin main", want: mergeguard.KindPushDefault},
		{name: "push with -o value", cmd: "git push -o ci.skip origin main", want: mergeguard.KindPushDefault},
		{name: "push with --repo", cmd: "git push --repo origin origin main", want: mergeguard.KindPushDefault},
		{name: "git -C and -c before push", cmd: "git -C /wt -c push.default=current push origin main", want: mergeguard.KindPushDefault},
		{name: "delete main", cmd: "git push origin --delete main", want: mergeguard.KindPushDefault},
		{name: "delete main by :", cmd: "git push origin :main", want: mergeguard.KindPushDefault},
		{name: "push --all", cmd: "git push --all origin", want: mergeguard.KindPushDefault},
		{name: "push --mirror", cmd: "git push --mirror", want: mergeguard.KindPushDefault},
		{name: "push a glob", cmd: "git push origin 'refs/heads/*:refs/heads/*'", want: mergeguard.KindPushDefault},
		{name: "quoted main", cmd: `git push origin "main"`, want: mergeguard.KindPushDefault},
		{name: "push to main in bash -c", cmd: `bash -c 'git push origin HEAD:main'`, want: mergeguard.KindPushDefault},
		{name: "bare push on main", cmd: "git push", git: onBranch("main", "main"), want: mergeguard.KindPushDefault},
		{name: "bare push of a branch tracking main", cmd: "git push", git: onBranch("feature-1", "main"), want: mergeguard.KindPushDefault},
		{name: "push HEAD while on main", cmd: "git push origin HEAD", git: onBranch("main", ""), want: mergeguard.KindPushDefault},
		{name: "push HEAD to main by $(...)", cmd: `git push origin "$(git branch --show-current)"`, git: onBranch("main", ""), want: mergeguard.KindPushDefault},
		{name: "push to a variable", cmd: "git push origin $BRANCH", want: mergeguard.KindPushUnknown},
		{name: "push to a computed branch", cmd: "git push origin $(cat branch.txt)", want: mergeguard.KindPushUnknown},

		{name: "push the feature branch", cmd: "git push -u origin feature-1", want: ""},
		{name: "push main into a feature branch", cmd: "git push origin main:feature-1", want: ""},
		{name: "bare push of the feature branch", cmd: "git push", want: ""},
		{name: "push HEAD on the feature branch", cmd: "git push origin HEAD", want: ""},
		{name: "push the current branch by $(...)", cmd: `git push -u origin "$(git branch --show-current)"`, want: ""},
		{name: "push tags", cmd: "git push --tags", want: ""},
		{name: "push a tag named main", cmd: "git push origin tag main", want: ""},
		{name: "push to refs/tags", cmd: "git push origin refs/tags/v1", want: ""},
		{name: "dry run to main", cmd: "git push -n origin main", want: ""},
		{name: "git log mentioning push", cmd: "git log --grep push", want: ""},
		{name: "a branch named like main", cmd: "git push origin main-fix", want: ""},

		// Where the parse gives up, a mention is enough.
		{name: "unparseable with merge", cmd: "gh pr merge 'unterminated", want: mergeguard.KindOpaque},
		{name: "unparseable without", cmd: "echo 'unterminated", want: ""},
		{name: "script on stdin", cmd: "echo 'gh pr merge' | bash", want: mergeguard.KindOpaque},
		{name: "heredoc to a shell", cmd: "bash <<'EOF'\ngit push origin main\nEOF", want: mergeguard.KindOpaque},
		{name: "computed command", cmd: "$GH pr merge", want: mergeguard.KindOpaque},
		{name: "computed command, innocent", cmd: "$EDITOR notes.txt", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			git := tc.git
			if git == nil {
				git = onBranch("feature-1", "feature-1")
			}
			got := mergeguard.NewClassifier(git).Command(context.Background(), "/wt", tc.cmd)
			if tc.want == "" {
				assert.Nil(t, got, "%q", tc.cmd)
				return
			}
			require.NotNil(t, got, "%q", tc.cmd)
			assert.Equal(t, tc.want, got.Kind, "%q: %s", tc.cmd, got.Detail)
			assert.NotEmpty(t, got.Detail)
		})
	}
}

func TestClassifyFollowsCd(t *testing.T) {
	t.Parallel()
	git := &fakeGit{answers: map[string]string{"/main-checkout: symbolic-ref --quiet --short HEAD": "main"}}
	c := mergeguard.NewClassifier(git)

	got := c.Command(context.Background(), "/wt", "cd /main-checkout && git push origin HEAD")
	require.NotNil(t, got, "the push runs where cd went, which is on main")
	assert.Equal(t, mergeguard.KindPushDefault, got.Kind)

	got = c.Command(context.Background(), "/", "git -C main-checkout push origin HEAD")
	require.NotNil(t, got, "-C is relative to the cwd")
	assert.Equal(t, mergeguard.KindPushDefault, got.Kind)
}

func TestClassifyAsksGitOnlyForAPushThatNeedsIt(t *testing.T) {
	t.Parallel()
	git := onBranch("feature-1", "feature-1")
	c := mergeguard.NewClassifier(git)
	assert.Nil(t, c.Command(context.Background(), "/wt", "go test ./... && gh pr view"))
	assert.Nil(t, c.Command(context.Background(), "/wt", "git push origin feature-1"))
	git.mu.Lock()
	defer git.mu.Unlock()
	assert.Equal(t, []string{"/wt: symbolic-ref --quiet --short refs/remotes/origin/HEAD"}, git.asked,
		"only the remote's default is looked up, for the named branch")
}

func TestClassifyWithoutARepoStillKnowsMainAndMaster(t *testing.T) {
	t.Parallel()
	c := mergeguard.NewClassifier(&fakeGit{})
	got := c.Command(context.Background(), "", "git push origin main")
	require.NotNil(t, got)
	assert.Equal(t, mergeguard.KindPushDefault, got.Kind)
	assert.Nil(t, c.Command(context.Background(), "", "git push"), "with no repo, a bare push's branch is unknown and git would fail anyway")
}

func TestClassifyTools(t *testing.T) {
	t.Parallel()
	c := mergeguard.NewClassifier(onBranch("feature-1", "feature-1"))
	cases := []struct {
		call mergeguard.Call
		want mergeguard.Kind
	}{
		{mergeguard.Call{Tool: "Bash", Command: "gh pr merge", Cwd: "/wt"}, mergeguard.KindPRMerge},
		{mergeguard.Call{Tool: "bash", Command: "gh pr merge", Cwd: "/wt"}, mergeguard.KindPRMerge},
		{mergeguard.Call{Tool: "Read", Command: "gh pr merge"}, ""},
		{mergeguard.Call{Tool: "mcp__github__merge_pull_request"}, mergeguard.KindTool},
		{mergeguard.Call{Tool: "mcp__github__get_pull_request"}, ""},
	}
	for _, tc := range cases {
		got := c.Classify(context.Background(), &tc.call)
		if tc.want == "" {
			assert.Nil(t, got, "%+v", tc.call)
			continue
		}
		require.NotNil(t, got, "%+v", tc.call)
		assert.Equal(t, tc.want, got.Kind)
	}
}
