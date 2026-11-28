package claudecode

import (
	"bytes"
	"context"
	"io"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
)

// screen is a TUI's terminal output, with the escape codes stripped, that
// a test waits on as it changes.
type screen struct {
	mu      sync.Mutex
	raw     bytes.Buffer
	changed chan struct{}
	closed  bool
}

var ansi = regexp.MustCompile(`\x1b\[[0-9;?<>=]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)|\x1b[@-_]`)

func (s *screen) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.raw.Write(p)
	close(s.changed)
	s.changed = make(chan struct{})
	return len(p), nil
}

func (s *screen) text() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	// The TUI lays text out with cursor moves, so spacing is lost: match
	// on the text with all whitespace removed.
	return strings.Join(strings.Fields(ansi.ReplaceAllString(s.raw.String(), "")), "")
}

// until waits for the screen, from offset on, to match; it returns where
// the match ended.
func (s *screen) until(t *testing.T, ctx context.Context, from int, match func(string) int) int {
	t.Helper()
	for {
		s.mu.Lock()
		ch := s.changed
		s.mu.Unlock()
		txt := s.text()
		if from < len(txt) {
			if at := match(txt[from:]); at >= 0 {
				return from + at
			}
		}
		select {
		case <-ch:
		case <-ctx.Done():
			require.FailNow(t, "the TUI never showed what was waited for", "screen:\n%s", txt[min(from, len(txt)):])
		}
	}
}

func contains(sub string) func(string) int {
	return func(s string) int {
		if i := strings.Index(s, sub); i >= 0 {
			return i + len(sub)
		}
		return -1
	}
}

// The hand-over round trip, live: programmatic, then the TUI on the same
// conversation in a terminal (what `slop-bot attach` runs), then
// programmatic again, the conversation surviving both.
func TestLiveHandOverToTheTUIAndBack(t *testing.T) {
	h, ws := live(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	conv, err := h.Start(ctx, ws, nil)
	require.NoError(t, err)
	defer func() { _ = conv.Stop(context.Background(), true) }()
	until(t, ctx, conv, harness.Ready, nil)
	require.NoError(t, conv.Send(ctx, &harness.Message{Text: "Remember the word KUMQUAT. Reply with just: PONG"}))
	until(t, ctx, conv, harness.TurnEnded, nil)
	plan, err := conv.AttachPlan(ctx)
	require.NoError(t, err)
	ref := conv.Ref()
	require.NoError(t, conv.Stop(ctx, false))
	until(t, ctx, conv, harness.Exited, nil)

	cmd := exec.CommandContext(ctx, plan.Command.Path, plan.Command.Args...) // #nosec G204 -- the harness's own TUI command
	cmd.Env, cmd.Dir = plan.Command.Env, plan.Command.Dir
	tty, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 40, Cols: 120})
	require.NoError(t, err)
	defer func() { _ = tty.Close() }()
	scr := &screen{changed: make(chan struct{})}
	go func() { _, _ = io.Copy(scr, tty) }()
	typeIn := func(s string) {
		_, err := tty.WriteString(s)
		require.NoError(t, err)
	}

	// A fresh folder asks for trust first ("No, exit" preselected).
	at := scr.until(t, ctx, 0, func(s string) int {
		if i := strings.Index(s, "trust"); i >= 0 {
			return i
		}
		return contains("bypasspermissionson")(s)
	})
	if !strings.Contains(scr.text(), "bypasspermissionson") {
		typeIn("\x1b[B")
		typeIn("\r")
		at = scr.until(t, ctx, at, contains("bypasspermissionson"))
	}

	// The person works in the TUI, on the same conversation.
	prompt := "What word did I ask you to remember? Reply with the word, then the number seven times six."
	typeIn(prompt)
	at = scr.until(t, ctx, at, contains("seventimessix"))
	typeIn("\r")
	at = scr.until(t, ctx, at, contains("42"))
	assert.Contains(t, strings.ToUpper(scr.text()), "KUMQUAT")
	typeIn("Also remember the word FIG. Reply with just: DONE")
	at = scr.until(t, ctx, at, contains("just:DONE"))
	typeIn("\r")
	scr.until(t, ctx, at, contains("DONE"))
	typeIn("/exit")
	scr.until(t, ctx, at, contains("/exit"))
	typeIn("\r")
	require.NoError(t, cmd.Wait(), "the TUI quits on /exit")

	// Back to programmatic, by the same id: the TUI's turn is in it.
	resumed, err := h.Start(ctx, ws, &ref)
	require.NoError(t, err)
	defer func() { _ = resumed.Stop(context.Background(), true) }()
	until(t, ctx, resumed, harness.Ready, nil)
	assert.Equal(t, ref.ID, resumed.Ref().ID)
	require.NoError(t, resumed.Send(ctx, &harness.Message{Text: "Which two words did I ask you to remember? Reply with just the two words."}))
	var text strings.Builder
	until(t, ctx, resumed, harness.TurnEnded, func(e harness.Event) { text.WriteString(e.Text) })
	assert.Contains(t, strings.ToUpper(text.String()), "KUMQUAT")
	assert.Contains(t, strings.ToUpper(text.String()), "FIG")
}
