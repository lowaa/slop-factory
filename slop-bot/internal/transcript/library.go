package transcript

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
	"github.com/lowaa/slop-factory/slop-bot/internal/state"
)

// Harnesses finds the implementation configured for a harness name.
type Harnesses interface {
	Harness(name string) (harness.AIHarness, bool)
}

// Library reads an agent's transcript from wherever it is: the harness's
// own file while that exists, else the Archive's copy.
type Library struct {
	harnesses Harnesses
	archive   *Archive
}

// NewLibrary is a Library over the configured harnesses and the Archive.
func NewLibrary(h Harnesses, a *Archive) *Library {
	return &Library{harnesses: h, archive: a}
}

// Read is a's whole transcript, normalised. The harness's copy is
// preferred: it carries what happened since a retained copy was made (an
// attach, say). ErrNotFound means neither exists.
func (l *Library) Read(ctx context.Context, a *state.Agent) ([]Entry, error) {
	entries, err := l.readLive(a)
	if err == nil || !errors.Is(err, ErrNotFound) {
		return entries, err
	}
	return l.archive.Load(ctx, a.ID)
}

// Retain copies a's normalised transcript into the Archive, for when the
// harness prunes its own. An agent with no transcript yet retains nothing.
func (l *Library) Retain(ctx context.Context, a *state.Agent) error {
	entries, err := l.readLive(a)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return l.archive.Keep(ctx, a.ID, a.Ticket, entries)
}

func (l *Library) readLive(a *state.Agent) ([]Entry, error) {
	if a.ConversationID == "" {
		return nil, fmt.Errorf("%w: agent %s has no conversation", ErrNotFound, a.ID)
	}
	h, ok := l.harnesses.Harness(a.Harness)
	if !ok {
		return nil, fmt.Errorf("%w: harness %q isn't configured", ErrNotFound, a.Harness)
	}
	p, ok := h.(Provider)
	if !ok {
		return nil, fmt.Errorf("%w: harness %q has no transcript reader", ErrNotFound, a.Harness)
	}
	r := p.Transcripts()
	path, err := r.Locate(a.ConversationID)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path) // #nosec G304 -- the harness's own transcript, which Locate found
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s is gone", ErrNotFound, path)
	}
	if err != nil {
		return nil, fmt.Errorf("transcript: %w", err)
	}
	return r.Parse(data)
}
