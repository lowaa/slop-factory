// Package briefing renders what an agent is told: slop-bot's own built-in
// briefing (embedded in the binary, so it always matches the CLI it
// describes), followed by the user's own briefing.
package briefing

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"text/template"
)

//go:embed builtin.md.tmpl
var builtinSource string

var builtin = template.Must(template.New("builtin").Parse(builtinSource))

// Ticket is the part of a ticket the briefing names.
type Ticket struct {
	ID        string
	Title     string
	AutoMerge bool
}

// Worktree is one of the ticket's worktrees.
type Worktree struct {
	Repo, Path, Branch string
	Primary            bool
}

// Repo is one entry of the repo lookup.
type Repo struct {
	Name, Path string
}

// Data is everything the built-in briefing is rendered from.
type Data struct {
	Ticket    Ticket
	Worktrees []Worktree
	Repos     []Repo
	// CLI is how the agent invokes slop-bot: an absolute path, so the agent
	// needn't have it on PATH.
	CLI                string
	Nudges             int
	Autonomy           bool
	AgentsMaySetUrgent bool
	// HarnessNotes are quirks of the agent's harness worth knowing (e.g.
	// Codex refuses rm -rf).
	HarnessNotes []string
	// CLIReference is generated from the CLI's command tree.
	CLIReference string
}

// ErrUserBriefing means the configured user briefing couldn't be read. It is
// never skipped silently: an agent quietly running without the user's
// workflow is worse than a loud failure.
var ErrUserBriefing = errors.New("briefing: the user's briefing can't be read")

// Render returns the built-in briefing for d, followed by the user's briefing
// at userPath ("" for none). It reads userPath on every call, so an edit
// applies to the next agent without a restart.
func Render(d *Data, userPath string) (string, error) {
	var buf bytes.Buffer
	if err := builtin.Execute(&buf, d); err != nil {
		return "", fmt.Errorf("briefing: rendering: %w", err)
	}
	if userPath == "" {
		return buf.String(), nil
	}
	user, err := os.ReadFile(userPath) // #nosec G304 -- the user configures their own briefing path
	if err != nil {
		return "", fmt.Errorf("%w: %s: %v", ErrUserBriefing, userPath, err)
	}
	buf.WriteString("\n---\n\n# The user's briefing\n\n")
	buf.Write(user)
	return buf.String(), nil
}

// Write renders the briefing and writes it to dir/briefing.md: the file the
// harness's system-prompt injection is made from, and its fallback. dir is
// outside the worktree, so nothing is written into the user's repo.
func Write(dir string, d *Data, userPath string) (path, text string, err error) {
	text, err = Render(d, userPath)
	if err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", err
	}
	path = filepath.Join(dir, "briefing.md")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		return "", "", err
	}
	return path, text, nil
}
