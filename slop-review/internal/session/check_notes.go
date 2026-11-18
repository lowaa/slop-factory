package session

import (
	"errors"
	"fmt"
	"strings"

	"github.com/lowaa/slop-factory/slop-review/internal/claudecli"
	"github.com/lowaa/slop-factory/slop-review/internal/llm"
)

// cliUsage is what a CLI session reports as its usage: the CLI prints one
// token total for the whole run and no split, so it is counted as one
// request's input -- the report's token fields then carry the session's
// size, and a subscription's session is not priced from a split we don't
// have.
func cliUsage(tokens int) llm.Usage {
	if tokens <= 0 {
		return llm.Usage{}
	}
	return llm.Usage{Requests: 1, InputTokens: int64(tokens)}
}

// readOnlyNote and runChecksNote close the CLI providers' tool notes. The
// base-commit rule in runChecksNote is the point of it: a suite that is
// red for reasons unrelated to the change (missing binaries, absent
// services) would otherwise be reported as a confident false finding.
const readOnlyNote = "- You cannot modify anything, and you do not need to.\n"

const runChecksNote = `- The repository is read-only and there is no network, but you can build, vet, type-check and run unit tests to check a claim instead of reasoning about it. A finding you have reproduced is worth several you reasoned your way to.
- Go's build cache and the temp directory ($TMPDIR) are writable; use the defaults, do not point GOCACHE or GOTMPDIR elsewhere. For Python set PYTHONDONTWRITEBYTECODE=1 and pass "-p no:cacheprovider" to pytest.
- Do not start Docker or anything that needs the network or the corporate mirrors. One failed attempt is diagnostic; more is a waste of the review.
- A failing test is not a finding until you have run it on the base commit too. Extract the base into a fresh temp dir, because git worktree and checkout cannot write to the read-only repository: "base=$(mktemp -d) && git -C <repo> archive <the Diffed against ref> <path> | tar -x -C \"$base\"" (the archive path is relative to the repository toplevel; archive the whole component directory; use a new mktemp -d for every check, since other sessions run at the same time) and run the same test there, then remove "$base". If it fails the same way on the base it is not this change's doing: leave it out.
`

func checkNotes(runChecks bool) string {
	if runChecks {
		return runChecksNote
	}
	return readOnlyNote
}

// cliErr wraps err from a CLI run for the caller, recognising the one
// failure a configured fallback exists for: a plan's usage limit. claude's
// runner recognises its own; codex prints it as free text ("You've hit your
// usage limit"), matched on that phrase alone because the error also
// carries the transcript's tail. A miss only means the failure is not
// taken for a limit.
func cliErr(who string, err error) error {
	if errors.Is(err, claudecli.ErrUsageLimit) || strings.Contains(strings.ToLower(err.Error()), "usage limit") {
		return fmt.Errorf("session: %s: %w: %w", who, llm.ErrUsageLimit, err)
	}
	return fmt.Errorf("session: %s: %w", who, err)
}
