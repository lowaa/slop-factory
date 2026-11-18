// Package persona loads the review persona library: markdown files under a
// persona directory, each one a system prompt describing how that persona
// should review a change set (see personas/standard.md).
package persona

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// nameRe matches the same charset as a persona flag/filename stem is
// expected to use -- rejecting anything else (path separators, "..") before
// it ever reaches a filesystem path built from user input.
var nameRe = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// Chunking is how a persona's review of a large change may be split.
type Chunking string

const (
	// ChunkByPackage splits the change into package-sized chunks, one
	// session each. The default: most review questions are answerable
	// from a package and whatever it calls.
	ChunkByPackage Chunking = "package"
	// ChunkWhole gives one session the whole change. For personas whose
	// question is cross-cutting by nature -- a trust boundary is rarely
	// inside one package -- and which lose the finding when split.
	ChunkWhole Chunking = "whole"
)

// Stage says when in a review a persona runs.
type Stage string

const (
	// StageReview is the default: the persona reviews the change alongside
	// the others.
	StageReview Stage = "review"
	// StageOverview runs before every review-stage persona, over the whole
	// change, and its result is handed to each of them as context (see
	// personas/blast-radius.md).
	StageOverview Stage = "overview"
)

type Persona struct {
	Name     string
	Prompt   string
	Chunking Chunking
	Stage    Stage
}

// AllPersonas is the --persona value that selects every persona in the
// library.
const AllPersonas = "all"

// Load reads <dir>/<name>.md. The file may open with front matter --
// "key: value" lines between two "---" lines -- which is stripped from
// the prompt; the keys are "chunking" and "stage".
func Load(dir, name string) (*Persona, error) {
	if !nameRe.MatchString(name) {
		return nil, fmt.Errorf("persona: invalid persona name %q", name)
	}
	path := filepath.Join(dir, name+".md")
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("persona: load %q: %w", name, err)
	}
	p := &Persona{Name: name, Prompt: string(content), Chunking: ChunkByPackage, Stage: StageReview}
	if err := p.applyFrontMatter(); err != nil {
		return nil, fmt.Errorf("persona: %q: %w", name, err)
	}
	return p, nil
}

// LoadMany resolves a --persona value: one name, a comma-separated list,
// or AllPersonas.
func LoadMany(dir, spec string) ([]*Persona, error) {
	var names []string
	if spec == AllPersonas {
		var err error
		if names, err = List(dir); err != nil {
			return nil, err
		}
	} else {
		for _, n := range strings.Split(spec, ",") {
			if n = strings.TrimSpace(n); n != "" {
				names = append(names, n)
			}
		}
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("persona: no personas selected by %q", spec)
	}

	seen := map[string]bool{}
	out := make([]*Persona, 0, len(names))
	for _, n := range names {
		if seen[n] {
			continue
		}
		seen[n] = true
		p, err := Load(dir, n)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// LoadOverviews loads every overview-stage persona in dir.
func LoadOverviews(dir string) ([]*Persona, error) {
	all, err := LoadMany(dir, AllPersonas)
	if err != nil {
		return nil, err
	}
	var out []*Persona
	for _, p := range all {
		if p.Stage == StageOverview {
			out = append(out, p)
		}
	}
	return out, nil
}

func (p *Persona) applyFrontMatter() error {
	// Normalised first: a persona saved with CRLF line endings would
	// otherwise not match "---\n", keep its front matter in the prompt,
	// and silently lose its chunking setting.
	p.Prompt = strings.ReplaceAll(p.Prompt, "\r\n", "\n")
	rest, ok := strings.CutPrefix(p.Prompt, "---\n")
	if !ok {
		return nil
	}
	header, body, ok := strings.Cut(rest, "\n---\n")
	if !ok {
		return fmt.Errorf("front matter is not closed with a \"---\" line")
	}
	for _, line := range strings.Split(header, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return fmt.Errorf("front matter line %q is not \"key: value\"", line)
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		switch key {
		case "chunking":
			switch c := Chunking(value); c {
			case ChunkByPackage, ChunkWhole:
				p.Chunking = c
			default:
				return fmt.Errorf("chunking %q must be %q or %q", value, ChunkByPackage, ChunkWhole)
			}
		case "stage":
			switch st := Stage(value); st {
			case StageReview, StageOverview:
				p.Stage = st
			default:
				return fmt.Errorf("stage %q must be %q or %q", value, StageReview, StageOverview)
			}
		default:
			return fmt.Errorf("unknown front matter key %q", key)
		}
	}
	p.Prompt = strings.TrimLeft(body, "\n")
	return nil
}

// List returns the persona names available under dir (filename stems of
// every *.md file), sorted.
func List(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("persona: list %q: %w", dir, err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if ext := filepath.Ext(e.Name()); ext == ".md" {
			names = append(names, e.Name()[:len(e.Name())-len(ext)])
		}
	}
	return names, nil
}
