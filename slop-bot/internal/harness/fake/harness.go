package fake

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/agentproc"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/rpcdriver"
	"github.com/lowaa/slop-factory/slop-bot/internal/transcript"
)

// Name and Driver of the fake, as tests register it.
const (
	Name   = "fake"
	Driver = "rpc"
)

// Harness runs the fakeharness binary as an rpc-driven harness.
type Harness struct {
	// Binary is the built fakeharness.
	Binary string
	// Control is the Controller's socket, or "" for an autonomous fake.
	Control string
	// TranscriptDir is where the fake keeps its transcripts; "" keeps none.
	TranscriptDir string
	Logger        *slog.Logger
	Clock         clock.Clock
}

// Name implements harness.AIHarness.
func (h *Harness) Name() string { return Name }

// Driver implements harness.AIHarness.
func (h *Harness) Driver() string { return Driver }

// Capabilities implements harness.AIHarness.
func (h *Harness) Capabilities() harness.Capabilities {
	return harness.Capabilities{MidTurnSteer: true}
}

// Start implements harness.AIHarness.
func (h *Harness) Start(ctx context.Context, ws *harness.Workspace, resume *harness.ConversationRef) (harness.Conversation, error) {
	return rpcdriver.Start(ctx, &rpcdriver.Options{
		Harness: Name, Driver: Driver, Codec: codec{}, Spec: launch(h.Binary, h.Control, h.TranscriptDir, ws, resume),
		Logger: h.Logger, Clock: h.Clock,
		// The hand-over: the same fake in its TUI mode, on the same
		// conversation, as `claude --resume <id>` is Claude Code's.
		Attach: func(id string) *harness.LaunchSpec {
			return launch(h.Binary, h.Control, h.TranscriptDir, ws, &harness.ConversationRef{Harness: Name, Driver: Driver, ID: id}, "--tui")
		},
	})
}

// launch is the fake's command line, in either mode (extra comes first).
// transcripts, when set, is where the fake keeps its transcript.
func launch(binary, control, transcripts string, ws *harness.Workspace, resume *harness.ConversationRef, extra ...string) *harness.LaunchSpec {
	set := map[string]string{}
	for k, v := range ws.Env {
		set[k] = v
	}
	if control != "" {
		set[EnvControl] = control
	}
	if transcripts != "" {
		set[EnvTranscripts] = transcripts
	}
	args := extra
	if resume != nil {
		args = append(args, "--resume", resume.ID)
	}
	return &harness.LaunchSpec{
		Path: binary,
		Args: args,
		Env:  agentproc.AgentEnv(&agentproc.EnvOptions{Base: os.Environ(), APIKeyOptIn: ws.APIKeyOptIn, Set: set}),
		Dir:  ws.Dir,
	}
}

// Transcripts implements transcript.Provider: the fake keeps its transcript
// in the normalised format itself.
func (h *Harness) Transcripts() transcript.Reader { return &transcriptReader{dir: h.TranscriptDir} }

type transcriptReader struct{ dir string }

func (r *transcriptReader) Locate(id string) (string, error) {
	if r.dir == "" {
		return "", transcript.ErrNotFound
	}
	return transcript.Find(id, func(id string) string { return filepath.Join(r.dir, id+".jsonl") })
}

func (r *transcriptReader) Parse(data []byte) ([]transcript.Entry, error) {
	return transcript.Decode(data)
}
