package bench

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/lowaa/slop-factory/slop-review/internal/review"
)

// Variant is one slop-review configuration under test: a name for the
// report, and the flags that configure it.
type Variant struct {
	Name string   `json:"name"`
	Args []string `json:"args"`
}

// ParseVariant reads "name=--flag --flag=value ...". An empty flag list
// is the tool's defaults.
func ParseVariant(spec string) (Variant, error) {
	name, args, ok := strings.Cut(spec, "=")
	name = strings.TrimSpace(name)
	if !ok || name == "" {
		return Variant{}, fmt.Errorf("bench: variant %q must be name=flags", spec)
	}
	return Variant{Name: name, Args: strings.Fields(args)}, nil
}

// RunSpec identifies one review within a benchmark sweep, and where/how to
// run it: which case, variant and run number it belongs to (slop-review
// tags its report as Kind bench with these when it's told to report at
// all -- see --bench-case/--bench-variant/--bench-run), the directory to
// review, and the variant's own extra flags.
type RunSpec struct {
	Case    string
	Variant string
	Run     int
	Dir     string
	Args    []string
}

// Reviewer runs one review, as specified by spec.
type Reviewer interface {
	Review(ctx context.Context, spec RunSpec) (*review.Review, error)
}

// ExecReviewer runs the slop-review binary -- the real entry point, flags
// and all, rather than the packages behind it, so a benchmark measures
// exactly what review.sh runs.
type ExecReviewer struct {
	Bin        string
	PersonaDir string
	Persona    string
	// LogDir, if set, receives each run's stderr (the per-session log).
	// Runs are numbered, so an ExecReviewer is not safe for concurrent use.
	LogDir string
	runs   int
}

func (e *ExecReviewer) Review(ctx context.Context, spec RunSpec) (*review.Review, error) {
	e.runs++
	full := []string{"--persona-dir=" + e.PersonaDir, "--repo-root=" + spec.Dir, "--persona=" + e.Persona}
	full = append(full, spec.Args...)
	// Tagged unconditionally: ExecReviewer only ever runs inside a
	// benchmark sweep, so the report server can tell a bench run from a
	// plain review whenever SLOP_REVIEW_REPORT_URL happens to be set in
	// the environment slop-review-bench runs under.
	full = append(full, "--bench-case="+spec.Case, "--bench-variant="+spec.Variant, "--bench-run="+strconv.Itoa(spec.Run))
	cmd := exec.CommandContext(ctx, e.Bin, full...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()

	if e.LogDir != "" {
		logPath := filepath.Join(e.LogDir, fmt.Sprintf("run-%03d.log", e.runs))
		if err := os.WriteFile(logPath, stderr.Bytes(), 0o644); err != nil {
			return nil, fmt.Errorf("bench: write run log: %w", err)
		}
	}
	if runErr != nil {
		return nil, fmt.Errorf("bench: slop-review: %w: %s", runErr, lastLine(stderr.String()))
	}

	var out struct {
		Review review.Review `json:"review"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		return nil, fmt.Errorf("bench: parse slop-review output: %w", err)
	}
	return &out.Review, nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

type RunResult struct {
	Score    *Score        `json:"score,omitempty"`
	Error    string        `json:"error,omitempty"`
	Duration time.Duration `json:"duration_ns"`
}

type VariantReport struct {
	Variant Variant     `json:"variant"`
	Runs    []RunResult `json:"runs"`
}

// Report is a whole benchmark: every variant's every run.
type Report struct {
	Case     string          `json:"case"`
	Bugs     []Bug           `json:"bugs"`
	Variants []VariantReport `json:"variants"`
}

type Runner struct {
	reviewer Reviewer
	logger   *slog.Logger
}

func NewRunner(reviewer Reviewer, logHandler slog.Handler) *Runner {
	return &Runner{reviewer: reviewer, logger: slog.New(logHandler)}
}

// Run reviews reviewDir under every variant, runs times each. Runs are
// repeated because a single review is one sample of a noisy process: a
// variant that catches a bug once in three runs is not the same as one
// that always does, and one run can't tell them apart.
//
// Variants are interleaved run by run rather than run back to back, so a
// provider-side change partway through (an outage, a model update behind
// the same slug) lands on every variant alike rather than on one.
func (r *Runner) Run(ctx context.Context, c *Case, reviewDir string, variants []Variant, runs int) *Report {
	report := &Report{Case: c.Name, Bugs: c.Bugs, Variants: make([]VariantReport, len(variants))}
	for i, v := range variants {
		report.Variants[i].Variant = v
	}
	for run := 1; run <= runs; run++ {
		for i, v := range variants {
			start := time.Now()
			result := RunResult{}
			rev, err := r.reviewer.Review(ctx, RunSpec{Case: c.Name, Variant: v.Name, Run: run, Dir: reviewDir, Args: v.Args})
			if err != nil {
				result.Error = err.Error()
			} else {
				result.Score = ScoreReview(c, rev)
			}
			result.Duration = time.Since(start)
			report.Variants[i].Runs = append(report.Variants[i].Runs, result)

			attrs := []any{"variant", v.Name, "run", run, "duration", result.Duration.Round(time.Second)}
			if result.Score != nil {
				attrs = append(attrs, "caught", result.Score.Caught, "of", result.Score.Total, "findings", result.Score.Findings)
			} else {
				attrs = append(attrs, "err", result.Error)
			}
			r.logger.Info("bench run finished", attrs...)
		}
	}
	return report
}
