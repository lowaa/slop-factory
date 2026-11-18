// Package bench measures slop-review against changes with known bugs in
// them: a real change from this repo's history, with bugs deliberately
// planted on top, reviewed under several configurations ("variants") and
// scored by which planted bugs each one found.
//
// It exists because the review tooling's own quality can't be judged from
// its output alone: a review that finds nothing looks the same whether the
// change was clean or the reviewer missed everything.
package bench

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Case is one benchmark change, loaded from a case.json.
type Case struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// Base and Head are commits in the source repository; the reviewed
	// change is Head's tree relative to Base's, plus Patch.
	Base string `json:"base"`
	Head string `json:"head"`
	// Patch plants the bugs, relative to the case file.
	Patch string `json:"patch"`
	// CommitMessage labels the synthetic commit. It must not give the
	// planted bugs away.
	CommitMessage string `json:"commit_message"`
	// ReviewDir is the repo-relative directory the review runs from,
	// the same way a human scopes review.sh by where they run it.
	ReviewDir string `json:"review_dir"`
	Bugs      []Bug  `json:"bugs"`

	dir string
}

// Bug is one planted defect, and where a finding must point to count as
// having caught it.
type Bug struct {
	ID          string     `json:"id"`
	Kind        string     `json:"kind"`
	Description string     `json:"description"`
	Locations   []Location `json:"locations"`
	// Keywords, if set, let a finding with no line number still count, as
	// long as it is about the right file and mentions one of them.
	Keywords []string `json:"keywords"`
}

type Location struct {
	File      string `json:"file"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
}

// LoadCase reads and validates a case.json.
func LoadCase(path string) (*Case, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("bench: read case: %w", err)
	}
	var c Case
	if err := json.Unmarshal(content, &c); err != nil {
		return nil, fmt.Errorf("bench: parse case %s: %w", path, err)
	}
	// Absolute, because the patch is applied from inside the new repo.
	if c.dir, err = filepath.Abs(filepath.Dir(path)); err != nil {
		return nil, fmt.Errorf("bench: resolve case dir: %w", err)
	}
	if err := c.validate(); err != nil {
		return nil, fmt.Errorf("bench: case %s: %w", path, err)
	}
	return &c, nil
}

// PatchPath is the absolute path of the case's patch, if it has one.
func (c *Case) PatchPath() string {
	if c.Patch == "" {
		return ""
	}
	return filepath.Join(c.dir, c.Patch)
}

func (c *Case) validate() error {
	if c.Name == "" || c.Base == "" || c.Head == "" {
		return fmt.Errorf("name, base and head are required")
	}
	if len(c.Bugs) == 0 {
		return fmt.Errorf("a case needs at least one bug to score against")
	}
	seen := map[string]bool{}
	for _, b := range c.Bugs {
		if b.ID == "" || seen[b.ID] {
			return fmt.Errorf("bug ids must be present and unique, got %q", b.ID)
		}
		seen[b.ID] = true
		if len(b.Locations) == 0 {
			return fmt.Errorf("bug %s has no locations", b.ID)
		}
		for _, l := range b.Locations {
			if l.File == "" || l.StartLine <= 0 || l.EndLine < l.StartLine {
				return fmt.Errorf("bug %s has an invalid location %+v", b.ID, l)
			}
		}
	}
	return nil
}
