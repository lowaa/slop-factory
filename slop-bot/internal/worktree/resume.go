package worktree

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
)

// resume is what a deleted worktree was on, kept while it's being re-added:
// git drops the stale registration, the only other record, before the add.
type resume struct {
	Branch string `json:"branch,omitempty"`
	// Head is set instead for a detached HEAD.
	Head string `json:"head,omitempty"`
}

// resumePath sits beside the worktree in the workspace, which Manager owns.
// The leading dot keeps it out of the t<ticket>-<repo> namespace.
func (m *Manager) resumePath(ticket ident.TicketID, repo string) string {
	return filepath.Join(m.workspace, "."+dirName(ticket, repo)+".resume")
}

func (m *Manager) writeResume(ticket ident.TicketID, repo string, r *resume) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if err := os.WriteFile(m.resumePath(ticket, repo), raw, 0o600); err != nil {
		return fmt.Errorf("worktree: recording what to resume: %w", err)
	}
	return nil
}

// readResume is the resume file for ticket and repo, or nil if there is none.
func (m *Manager) readResume(ticket ident.TicketID, repo string) (*resume, error) {
	raw, err := os.ReadFile(m.resumePath(ticket, repo))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("worktree: reading what to resume: %w", err)
	}
	r := &resume{}
	if err := json.Unmarshal(raw, r); err != nil {
		return nil, fmt.Errorf("worktree: reading what to resume from %s: %w", m.resumePath(ticket, repo), err)
	}
	return r, nil
}

// dropResume removes the resume file. A leftover one only makes a later
// fresh Create pick up the branch it names, so failing to remove it is
// logged, not returned.
func (m *Manager) dropResume(ticket ident.TicketID, repo string) {
	if err := os.Remove(m.resumePath(ticket, repo)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		m.logger.Warn("couldn't remove a worktree resume file", "path", m.resumePath(ticket, repo), "err", err)
	}
}
