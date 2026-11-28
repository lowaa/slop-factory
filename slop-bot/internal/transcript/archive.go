package transcript

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/state"
)

// Index is where retained transcripts are recorded: state.db's transcripts
// table.
type Index interface {
	Add(ctx context.Context, n *state.NewTranscript) error
	ForAgent(ctx context.Context, agent ident.AgentID) ([]state.Transcript, error)
	Expired(ctx context.Context, at time.Time) ([]state.Transcript, error)
	Remove(ctx context.Context, path string) error
}

// ArchiveOptions configures an Archive.
type ArchiveOptions struct {
	// Dir holds the retained files: $XDG_STATE_HOME/slop-bot/transcripts.
	Dir   string
	Index Index
	// Retain is how long a transcript is kept after its agent ends.
	Retain time.Duration
	Clock  clock.Clock
}

// Archive keeps normalised transcripts after their agents end, and deletes
// each once its retention is over, when Sweep is run (the daemon's
// retention sweep runs it on the clock).
type Archive struct {
	opts *ArchiveOptions
}

// NewArchive makes an Archive.
func NewArchive(opts *ArchiveOptions) (*Archive, error) {
	if opts == nil || opts.Dir == "" || opts.Index == nil || opts.Clock == nil || opts.Retain <= 0 {
		return nil, errors.New("transcript: NewArchive needs Dir, Index, a positive Retain, and Clock")
	}
	return &Archive{opts: opts}, nil
}

// Keep writes agent's transcript and indexes it until the retention ends.
// Keeping an agent's transcript again replaces it, and restarts its
// retention.
func (a *Archive) Keep(ctx context.Context, agent ident.AgentID, ticket ident.TicketID, entries []Entry) error {
	if !validID.MatchString(string(agent)) {
		return fmt.Errorf("transcript: %q isn't an agent id", agent)
	}
	if err := os.MkdirAll(a.opts.Dir, 0o700); err != nil {
		return fmt.Errorf("transcript: %w", err)
	}
	var buf bytes.Buffer
	if err := Encode(&buf, entries); err != nil {
		return err
	}
	path := filepath.Join(a.opts.Dir, string(agent)+".jsonl")
	// Written aside, then renamed: a reader never sees half a transcript.
	tmp, err := os.CreateTemp(a.opts.Dir, "."+string(agent)+"-*")
	if err != nil {
		return fmt.Errorf("transcript: %w", err)
	}
	_, werr := tmp.Write(buf.Bytes())
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("transcript: writing %s: %w", path, err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("transcript: %w", err)
	}
	return a.opts.Index.Add(ctx, &state.NewTranscript{
		Path: path, Agent: agent, Ticket: ticket, RetainUntil: a.opts.Clock.Now().Add(a.opts.Retain),
	})
}

// Load is agent's retained transcript, or ErrNotFound.
func (a *Archive) Load(ctx context.Context, agent ident.AgentID) ([]Entry, error) {
	kept, err := a.opts.Index.ForAgent(ctx, agent)
	if err != nil {
		return nil, err
	}
	if len(kept) == 0 {
		return nil, fmt.Errorf("%w: nothing retained for agent %s", ErrNotFound, agent)
	}
	path := kept[len(kept)-1].Path
	data, err := os.ReadFile(path) // #nosec G304 -- a path the Archive wrote and indexed
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s is gone", ErrNotFound, path)
	}
	if err != nil {
		return nil, fmt.Errorf("transcript: %w", err)
	}
	return Decode(data)
}

// Sweep deletes every transcript past its retention: the file first, then
// its index entry, so a failure leaves it to the next sweep.
func (a *Archive) Sweep(ctx context.Context) error {
	expired, err := a.opts.Index.Expired(ctx, a.opts.Clock.Now())
	if err != nil {
		return err
	}
	var errs []error
	for _, t := range expired {
		if err := os.Remove(t.Path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
			continue
		}
		if err := a.opts.Index.Remove(ctx, t.Path); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
