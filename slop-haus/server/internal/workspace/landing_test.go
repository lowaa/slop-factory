package workspace_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/workspace"
)

func flatten(s string) string { return strings.Join(strings.Fields(s), " ") }

// landingFiles is every file that tells an agent how a change lands, rendered
// for a tenant with (changeTools) or without a code host credential. The
// headless briefing carries the worker rules itself, so there is no separate copy.
func landingFiles(t *testing.T, changeTools bool) map[string]string {
	t.Helper()
	files, err := workspace.Files(workspace.Params{AgentName: "builder-1", Server: "https://s", ChangeTools: changeTools})
	require.NoError(t, err)
	out := map[string]string{}
	for _, f := range files {
		switch f.Path {
		case workspace.HeadlessBriefing:
			out[f.Path] = flatten(string(f.Body))
		}
	}
	require.Len(t, out, 1)
	return out
}

func TestLanding_EveryVariantCarriesTheLockDisciplineAndTheRerunRule(t *testing.T) {
	for _, changeTools := range []bool{true, false} {
		for path, body := range landingFiles(t, changeTools) {
			assert.Contains(t, body, "`integration_unlock`", path)
			assert.Contains(t, body, "a flaky check is re-run outside the lock, and a second failure goes to", path)
			assert.Contains(t, body, "before the lock", path)
			assert.Contains(t, body, "under the lock only re-check", path)
			assert.Contains(t, body, "`ticket_discuss`", path)
		}
	}
}

func TestLanding_WithACredentialTheChangeToolsDriveTheFlow(t *testing.T) {
	for path, body := range landingFiles(t, true) {
		for _, tool := range []string{"change_open", "change_checks", "change_merge", "integration_lock", "integration_unlock", "ticket_finish"} {
			assert.Contains(t, body, tool, path)
		}
		assert.Contains(t, body, "the host's own CLI", path)
		assert.NotContains(t, body, "the repo's CLAUDE.md says which tool to use", path)
		// A credential disconnected after the file was written must not leave
		// the agent without a way to land its change.
		assert.Contains(t, body, "no code host credential", path)
	}
	assert.Contains(t, landingFiles(t, true)[workspace.HeadlessBriefing],
		"wait for `change_checks` (with the commit you pushed as `head_sha`) to say they passed")
	assert.Contains(t, landingFiles(t, true)[workspace.HeadlessBriefing],
		"release the lock with `integration_unlock`, resolve and test outside it")
}

func TestLanding_WithoutACredentialTheAgentUsesTheReposOwnTool(t *testing.T) {
	for path, body := range landingFiles(t, false) {
		for _, tool := range []string{"change_open", "change_checks", "change_merge"} {
			// The headless waiting rule names change_checks in both variants.
			if path == workspace.HeadlessBriefing && tool == "change_checks" {
				continue
			}
			assert.NotContains(t, body, tool, path)
		}
	}
	assert.Contains(t, landingFiles(t, false)[workspace.HeadlessBriefing],
		"The repo's CLAUDE.md says which tool to use and how")
}

// The server's conversation checkout is the PR's head, so the PR must exist
// from the first push, and must be out of draft before the merge.
func TestLanding_TheChangeIsOpenedAsADraftAtTheFirstPushAndReadiedBeforeTheLock(t *testing.T) {
	for _, changeTools := range []bool{true, false} {
		for path, body := range landingFiles(t, changeTools) {
			draft := strings.Index(body, "draft")
			lock := strings.Index(body, "`integration_lock`")
			require.GreaterOrEqual(t, draft, 0, path)
			assert.Less(t, draft, lock, "%s: the draft comes before the lock", path)
			// Looked for in order, each after the one before, so a draft
			// opened after the lock, or never readied, fails.
			rest := body
			for _, step := range []string{"first push", "draft", "push as the work goes", "out of draft", "`integration_lock`"} {
				at := strings.Index(rest, step)
				require.GreaterOrEqual(t, at, 0, "%s: step %q is missing or out of order", path, step)
				rest = rest[at+len(step):]
			}
		}
	}
	assert.Contains(t, landingFiles(t, true)[workspace.HeadlessBriefing], "`change_open` with it and `draft` true")
	assert.Contains(t, landingFiles(t, true)[workspace.HeadlessBriefing], "same branch and `draft` false")
	assert.Contains(t, landingFiles(t, false)[workspace.HeadlessBriefing], "as a draft where the tool has drafts")
	assert.NotContains(t, landingFiles(t, false)[workspace.HeadlessBriefing], "`draft` true")
}

func TestLanding_AnApprovalTicketIsHandedOverOutOfDraft(t *testing.T) {
	for _, changeTools := range []bool{true, false} {
		for path, body := range landingFiles(t, changeTools) {
			i := strings.Index(body, "requires human approval")
			require.GreaterOrEqual(t, i, 0, path)
			rest := body[i:]
			assert.Contains(t, rest[:strings.Index(rest, "`ticket_finish`")], "out of draft", path)
		}
	}
}

func TestLanding_BriefingsAreDeterministicForEachCase(t *testing.T) {
	assert.Equal(t, workspace.HeadlessBriefingText("a", nil, true), workspace.HeadlessBriefingText("a", nil, true))
	assert.NotEqual(t, workspace.HeadlessBriefingText("a", nil, true), workspace.HeadlessBriefingText("a", nil, false))
}

// Main is merged in and tested before the lock, which is held for a re-check
// and the merge only; an order the other way would serialise CI under it.
func TestLanding_MainIsMergedAndTestedBeforeTheLock(t *testing.T) {
	for _, changeTools := range []bool{true, false} {
		for path, body := range landingFiles(t, changeTools) {
			rest := body
			for _, step := range []string{"out of draft", "merge the base branch into your branch", "re-run the tests", "push", "before the lock", "take `integration_lock`", "under the lock only re-check", "merge the change"} {
				if changeTools && step == "merge the change" {
					step = "`change_merge`"
				}
				at := strings.Index(rest, step)
				require.GreaterOrEqual(t, at, 0, "%s: step %q is missing or out of order", path, step)
				rest = rest[at+len(step):]
			}
		}
	}
}

// A voluntary review is rare, needs a reason, and is for a finished change;
// a question about direction goes to ticket_discuss before building.
func TestLanding_AVoluntaryReviewIsRareNeedsAReasonAndIsNotHowToAsk(t *testing.T) {
	check := func(name, body string) {
		assert.Contains(t, body, "On a ticket that does not require approval you merge the change yourself.", name)
		assert.Contains(t, body, "`ticket_finish` with `request_review` there is rare and needs a `reason`", name)
		assert.Contains(t, body, "for a finished change whose code you want a human to read before it merges", name)
		assert.Contains(t, body, "A question about direction, spec or approach is not a review: ask it early, before building it, with `ticket_discuss`.", name)
	}
	for _, changeTools := range []bool{true, false} {
		for path, body := range landingFiles(t, changeTools) {
			check(path, body)
		}
		check("headless", flatten(workspace.HeadlessBriefingText("builder-1", nil, changeTools)))
	}
}

// Main moves faster than a CI run, so "merge main before the lock" needs an
// end: merge and go green once, then take the lock even if main has moved.
func TestLanding_MergingMainBeforeTheLockHappensOnce(t *testing.T) {
	for _, changeTools := range []bool{true, false} {
		for path, body := range landingFiles(t, changeTools) {
			assert.Contains(t, body, "Do this once: merge the base branch and get the checks green a single time, then take the lock even if the base branch has moved since", path)
			assert.Contains(t, body, "never merge the base branch again before the lock just because it moved", path)
			assert.Contains(t, body, "never to avoid waiting in the lock's queue", path)
			assert.Contains(t, body, "if the lock is held, queue for it", path)
		}
	}
}

func TestLanding_AHeadlessRunEndsItsTurnAtAWaitInsteadOfReleasing(t *testing.T) {
	for _, changeTools := range []bool{true, false} {
		body := flatten(workspace.HeadlessBriefingText("a", nil, changeTools))
		assert.Contains(t, body, "At a wait point (checks pending, lock queued, review requested) end your turn and keep the lease")
		assert.Contains(t, body, "Never use `ticket_release` with a cooldown to wait for CI, the lock or a review")
	}
}

// The briefing is written once per workspace, so it can't name a branch: the
// ticket's own base arrives in the tool results. A hardcoded main here would
// send a tenant with a base branch to the wrong one.
func TestBriefings_NameNoBranchOfTheirOwn(t *testing.T) {
	for name, text := range map[string]string{
		"headless":    workspace.HeadlessBriefingText("a", nil, true),
		"headless-no": workspace.HeadlessBriefingText("a", nil, false),
		"steward":     workspace.StewardBriefingText("a", nil, nil),
	} {
		assert.NotContains(t, text, "origin/main", name)
		if name != "steward" {
			assert.Contains(t, text, "<base> is the ticket's base branch", name)
		}
		assert.NotRegexp(t, `\b(merge|merging|into|on|to|reaches|if|even if) main\b`, text, name)
	}
}
