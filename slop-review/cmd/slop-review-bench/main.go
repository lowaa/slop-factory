// Command slop-review-bench scores slop-review configurations against a
// benchmark case: a real change with known bugs planted in it. See
// bench.sh for the intended entry point.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/spf13/pflag"

	"github.com/lowaa/slop-factory/slop-review/internal/bench"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "slop-review-bench:", err)
		os.Exit(1)
	}
}

// variantSets are the named comparisons --variant-set selects when no
// --variant is given.
var variantSets = map[string][]string{
	// shapes compares the tool's default, one session over the whole diff,
	// against splitting a change into package-sized chunks with a seam
	// pass over them.
	"shapes": {
		"single=",
		"chunked=--chunk-diff-bytes=60000 --max-diff-bytes=200000 --context-budget-bytes=400000",
	},
	// providers compares gpt-5.6-luna through OpenRouter with the same
	// model through a ChatGPT subscription, each at its provider's default
	// model and effort -- what deciding the default provider rests on.
	"providers": {
		"openrouter=--provider=openrouter",
		"chatgpt=--provider=chatgpt",
	},
}

type options struct {
	casePath   string
	sourceRepo string
	bin        string
	personaDir string
	persona    string
	variants   []string
	variantSet string
	runs       int
	workDir    string
}

func run(args []string, stdout, stderr io.Writer) error {
	opts := options{}
	fs := pflag.NewFlagSet("slop-review-bench", pflag.ContinueOnError)
	fs.StringVar(&opts.casePath, "case", "", "path to a case.json (required)")
	fs.StringVar(&opts.sourceRepo, "source-repo", ".", "repository the case's commits live in")
	fs.StringVar(&opts.bin, "slop-review-bin", "", "slop-review binary to benchmark (required)")
	fs.StringVar(&opts.personaDir, "persona-dir", "personas", "persona library")
	fs.StringVar(&opts.persona, "persona", "standard", "--persona passed to every run, before the variant's own flags (which may override it)")
	fs.StringArrayVar(&opts.variants, "variant", nil, `a configuration to score, as "name=flags" (repeatable; overrides --variant-set)`)
	fs.StringVar(&opts.variantSet, "variant-set", "shapes", `a named comparison: "shapes" (single vs chunked) or "providers" (OpenRouter vs ChatGPT subscription)`)
	fs.IntVar(&opts.runs, "runs", 1, "reviews per variant")
	fs.StringVar(&opts.workDir, "work-dir", "", "where to build the case repo and keep run logs (default: a temp dir, removed afterwards)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if opts.casePath == "" || opts.bin == "" {
		return fmt.Errorf("--case and --slop-review-bin are required")
	}
	if opts.runs <= 0 {
		return fmt.Errorf("--runs must be positive")
	}
	if len(opts.variants) == 0 {
		set, ok := variantSets[opts.variantSet]
		if !ok {
			return fmt.Errorf("--variant-set must be one of shapes, providers; got %q", opts.variantSet)
		}
		opts.variants = set
	}

	c, err := bench.LoadCase(opts.casePath)
	if err != nil {
		return err
	}
	var variants []bench.Variant
	for _, spec := range opts.variants {
		v, err := bench.ParseVariant(spec)
		if err != nil {
			return err
		}
		variants = append(variants, v)
	}

	workDir := opts.workDir
	if workDir == "" {
		if workDir, err = os.MkdirTemp("", "slop-review-bench-"); err != nil {
			return err
		}
		defer func() { _ = os.RemoveAll(workDir) }()
	}
	logger := slog.New(slog.NewTextHandler(stderr, nil))

	repo, err := bench.BuildRepo(opts.sourceRepo, c, workDir)
	if err != nil {
		return err
	}
	logger.Info("case repo built", "case", c.Name, "repo", repo)

	logDir := filepath.Join(workDir, "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return err
	}
	personaDir, err := filepath.Abs(opts.personaDir)
	if err != nil {
		return err
	}
	reviewer := &bench.ExecReviewer{Bin: opts.bin, PersonaDir: personaDir, Persona: opts.persona, LogDir: logDir}
	report := bench.NewRunner(reviewer, slog.NewTextHandler(stderr, nil)).
		Run(context.Background(), c, filepath.Join(repo, c.ReviewDir), variants, opts.runs)

	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(struct {
		Report   *bench.Report `json:"report"`
		Markdown string        `json:"markdown"`
	}{report, bench.RenderMarkdown(report)})
}
