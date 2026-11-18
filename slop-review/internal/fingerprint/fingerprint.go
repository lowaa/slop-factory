// Package fingerprint hashes what a slop-review run actually ran with, so
// runs can be grouped by the reviewer's content rather than by the git SHA
// of whatever checkout happened to hold it: that SHA changes with every
// unrelated commit, and is lost when the commit is never merged.
//
// Hashes are over file contents read from disk, so they are the same in any
// checkout of the same content, with or without uncommitted edits and
// without git.
package fingerprint

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/lowaa/slop-factory/slop-review/internal/persona"
	"github.com/lowaa/slop-factory/slop-review/report"
)

// Persona is one persona file of the library.
type Persona struct {
	Hash string
	// Text is the file as hashed, so a hash can be turned back into the
	// text it stands for.
	Text string
}

// Result is the fingerprint of one reviewer, as run.
type Result struct {
	// Run covers Source and every persona in the library, so it changes
	// when either does. Empty when no source directory was given.
	Run string
	// Source covers the reviewer's Go source and the text it embeds (the
	// briefing and prompts, seams.md), but not the personas.
	Source string
	// Personas is the whole persona library by name, not only the personas
	// a run selected: a run's fingerprint must not depend on which of them
	// it asked for.
	Personas map[string]Persona
}

// Compute fingerprints the reviewer whose source is under sourceDir (empty:
// skip Source and Run) and whose personas are the .md files in personaDir.
func Compute(sourceDir, personaDir string) (*Result, error) {
	res := &Result{Personas: map[string]Persona{}}
	names, err := persona.List(personaDir)
	if err != nil {
		return nil, err
	}
	slices.Sort(names)
	for _, name := range names {
		raw, err := os.ReadFile(filepath.Join(personaDir, name+".md"))
		if err != nil {
			return nil, fmt.Errorf("fingerprint: %w", err)
		}
		text := normalize(string(raw))
		res.Personas[name] = Persona{Hash: report.HashText(text), Text: text}
	}
	if sourceDir == "" {
		return res, nil
	}
	if res.Source, err = hashSource(sourceDir); err != nil {
		return nil, err
	}
	h := sha256.New()
	writeField(h, "source", res.Source)
	for _, name := range names {
		writeField(h, name, res.Personas[name].Hash)
	}
	res.Run = hex.EncodeToString(h.Sum(nil))
	return res, nil
}

// normalize makes line endings irrelevant, as persona loading does: a
// checkout with CRLF endings is the same persona.
func normalize(s string) string { return strings.ReplaceAll(s, "\r\n", "\n") }

type writer interface{ Write([]byte) (int, error) }

// writeField hashes a name/value pair unambiguously, by length-prefixing
// both so no two different sets of fields can concatenate to the same bytes.
func writeField(w writer, name, value string) {
	_, _ = fmt.Fprintf(w, "%d:%s%d:%s", len(name), name, len(value), value)
}

// skippedDirs are directories directly under the source root that do not
// change what the reviewer does: the personas are fingerprinted on their
// own, and bench holds sample cases and sweep output.
var skippedDirs = map[string]bool{"personas": true, "bench": true}

// hashSource hashes the reviewer's Go source, module files and embedded
// markdown: everything that decides what the model is told and how a run is
// orchestrated. Tests and testdata are left out, since editing them does not
// change a review.
func hashSource(root string) (string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skippedDirs[rel] || d.Name() == "testdata" || (path != root && strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		switch filepath.Ext(d.Name()) {
		case ".go", ".md", ".mod", ".sum":
			files = append(files, rel)
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("fingerprint: walk %q: %w", root, err)
	}
	if len(files) == 0 {
		return "", fmt.Errorf("fingerprint: no reviewer source under %q", root)
	}
	slices.Sort(files)
	h := sha256.New()
	for _, rel := range files {
		raw, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			return "", fmt.Errorf("fingerprint: %w", err)
		}
		writeField(h, filepath.ToSlash(rel), normalize(string(raw)))
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
