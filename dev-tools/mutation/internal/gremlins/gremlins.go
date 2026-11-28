// Package gremlins reads the machine-readable result of one gremlins run,
// the only part of the tool this repo depends on besides its command line.
package gremlins

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
)

// The statuses gremlins writes (internal/mutator in the pinned release).
const (
	Killed     = "KILLED"
	Lived      = "LIVED"
	TimedOut   = "TIMED OUT"
	NotCovered = "NOT COVERED"
	NotViable  = "NOT VIABLE"
)

// Mutation is one mutant and what the tests did with it.
type Mutation struct {
	Type   string `json:"type"`
	Status string `json:"status"`
	Line   int    `json:"line"`
	Column int    `json:"column"`
}

// File is the mutants of one source file. Name is relative to the directory
// gremlins was pointed at.
type File struct {
	Name      string     `json:"file_name"`
	Mutations []Mutation `json:"mutations"`
}

// Report is one gremlins run.
type Report struct {
	Files []File `json:"files"`
}

// Counts tallies mutants by outcome.
type Counts struct {
	Killed     int `json:"killed"`
	Lived      int `json:"lived"`
	TimedOut   int `json:"timed_out"`
	NotCovered int `json:"not_covered"`
	NotViable  int `json:"not_viable"`
}

// Add folds o into c.
func (c *Counts) Add(o Counts) {
	c.Killed += o.Killed
	c.Lived += o.Lived
	c.TimedOut += o.TimedOut
	c.NotCovered += o.NotCovered
	c.NotViable += o.NotViable
}

// Decided is the mutants a test run ruled on: not-covered and not-viable
// ones say nothing about the tests' strength.
func (c *Counts) Decided() int { return c.Killed + c.Lived + c.TimedOut }

// Score is the percentage of decided mutants the tests caught. A timeout
// counts as caught, as in PIT and Stryker: a mutant that hangs the suite
// is noticed. Ok is false when nothing was decided.
func (c *Counts) Score() (score float64, ok bool) {
	if c.Decided() == 0 {
		return 0, false
	}
	return 100 * float64(c.Killed+c.TimedOut) / float64(c.Decided()), true
}

// Survivor is a mutant no test caught.
type Survivor struct {
	File   string
	Line   int
	Column int
	Type   string
}

// Parse reads a report.
func Parse(r io.Reader) (*Report, error) {
	var rep Report
	if err := json.NewDecoder(r).Decode(&rep); err != nil {
		return nil, fmt.Errorf("reading the gremlins report: %w", err)
	}
	return &rep, nil
}

// ReadFile reads the report at path.
func ReadFile(path string) (*Report, error) {
	f, err := os.Open(path) // #nosec G304 -- a file mutation.sh just had gremlins write
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	rep, err := Parse(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return rep, nil
}

// Counts tallies every mutant in the report.
func (r *Report) Counts() Counts {
	var c Counts
	for _, f := range r.Files {
		for _, m := range f.Mutations {
			switch m.Status {
			case Killed:
				c.Killed++
			case Lived:
				c.Lived++
			case TimedOut:
				c.TimedOut++
			case NotCovered:
				c.NotCovered++
			case NotViable:
				c.NotViable++
			}
		}
	}
	return c
}

// Survivors lists the LIVED mutants by file and position.
func (r *Report) Survivors() []Survivor {
	var out []Survivor
	for _, f := range r.Files {
		for _, m := range f.Mutations {
			if m.Status == Lived {
				out = append(out, Survivor{File: f.Name, Line: m.Line, Column: m.Column, Type: m.Type})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Column < b.Column
	})
	return out
}
