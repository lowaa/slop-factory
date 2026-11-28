package cli

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/harness/conformance/scripted"
)

// tuiScript is how a person gets through a harness's TUI in an attach: what
// to settle before it starts, how it shows it is ready, and how to quit it.
// Markers are matched with whitespace removed, since a TUI lays text out
// with cursor moves.
type tuiScript struct {
	// prepare, when set, runs before the ticket's agent first starts, with
	// the worktree its TUI will open in.
	prepare func(t *testing.T, tg *scripted.Target, worktree string)
	ready   string
	// quit is typed, then Enter once it shows.
	quit string
}

const keyEnter = "\r"

var (
	// Claude Code asks to trust each new folder, a worktree included
	// (scripted.Target.TrustClaudeFolder says why it isn't answered on the
	// screen); its other first-run questions are answered in its config
	// dir (scripted.ClaudeCode).
	claudeTUI = &tuiScript{
		prepare: func(t *testing.T, tg *scripted.Target, worktree string) { tg.TrustClaudeFolder(t, worktree) },
		ready:   "bypasspermissionson",
		quit:    "/exit",
	}
	// Codex asks nothing of a folder in a CODEX_HOME of its own.
	codexTUI = &tuiScript{ready: "forshortcuts", quit: "/quit"}
	// pi's footer shows the context window once the session is loaded.
	piTUI = &tuiScript{ready: "/128k", quit: "/quit"}
)

// opened waits for the TUI to be ready and to show history, so it resumed
// the agent's conversation, and returns where on the screen it is.
func (s *tuiScript) opened(ctx context.Context, term *terminal, history string) int {
	term.t.Helper()
	at := term.until(ctx, 0, contains(s.ready))
	term.until(ctx, 0, contains(history))
	return at
}

// quitAt quits the TUI opened returned at.
func (s *tuiScript) quitAt(ctx context.Context, term *terminal, at int) {
	term.t.Helper()
	term.typeRaw(s.quit)
	term.until(ctx, at, contains(s.quit))
	term.typeRaw(keyEnter)
}

func contains(sub string) func(string) int {
	return func(s string) int {
		if i := strings.Index(s, sub); i >= 0 {
			return i + len(sub)
		}
		return -1
	}
}

// typeRaw types s at the terminal as it is: keys, not a line.
func (term *terminal) typeRaw(s string) {
	term.t.Helper()
	_, err := term.pty.WriteString(s)
	require.NoError(term.t, err)
}

var ansi = regexp.MustCompile(`\x1b\[[0-9;?<>=]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)|\x1b[@-_]`)

// text is the screen with escape codes and whitespace removed.
func (term *terminal) text() string {
	return strings.Join(strings.Fields(ansi.ReplaceAllString(term.screen(), "")), "")
}

// until waits for the screen's text, from offset on, to match, and returns
// where the match ended.
func (term *terminal) until(ctx context.Context, from int, match func(string) int) int {
	term.t.Helper()
	for {
		// wrote holds a signal for any write since it was last taken, so
		// one that lands after this read still wakes the wait below.
		txt := term.text()
		if from < len(txt) {
			if at := match(txt[from:]); at >= 0 {
				return from + at
			}
		}
		select {
		case <-term.wrote:
		case <-ctx.Done():
			require.FailNow(term.t, "the TUI never showed what was waited for", "screen:\n%s", txt[min(from, len(txt)):])
		}
	}
}
