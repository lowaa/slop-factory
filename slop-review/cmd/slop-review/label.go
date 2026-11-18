package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/spf13/pflag"

	"github.com/lowaa/slop-factory/slop-review/internal/config"
	"github.com/lowaa/slop-factory/slop-review/report/reporter"
)

var labelVerdicts = []string{"confirmed", "false_positive", "fabricated", "duplicate", "wont_fix"}

// runLabel records Claude's verdict on a finding of an earlier review, by
// the id that review printed. A usage mistake is an error; failing to reach
// the server is only a warning, since the verdict is already in the agent's
// own notes and a label must never get in the way of the work it describes.
func runLabel(ctx context.Context, args []string, stdout, stderr io.Writer, logHandler slog.Handler) error {
	fs := pflag.NewFlagSet("slop-review label", pflag.ContinueOnError)
	fs.SetOutput(stderr)
	verdict := fs.String("verdict", "", "one of: "+strings.Join(labelVerdicts, ", "))
	reason := fs.String("reason", "", "why")
	fixedIn := fs.String("fixed-in", "", "commit that fixed the finding (confirmed only)")
	reportTo := fs.String("report-to", os.Getenv("SLOP_REVIEW_REPORT_URL"), "report server URL; also SLOP_REVIEW_REPORT_URL")
	fs.String("report-token-command", os.Getenv("SLOP_REVIEW_REPORT_TOKEN_COMMAND"), "executable whose output is the token; also SLOP_REVIEW_REPORT_TOKEN_COMMAND")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("label: %w", err)
	}
	if fs.NArg() != 1 {
		return errors.New("label: usage: slop-review label <finding-id> --verdict=<verdict> [--reason=...] [--fixed-in=<commit>]")
	}
	findingID, err := uuid.Parse(fs.Arg(0))
	if err != nil {
		return fmt.Errorf("label: %q is not a finding id (the id: shown in the review)", fs.Arg(0))
	}
	if !slices.Contains(labelVerdicts, *verdict) {
		return fmt.Errorf("label: --verdict must be one of %s", strings.Join(labelVerdicts, ", "))
	}
	if *fixedIn != "" && *verdict != "confirmed" {
		return errors.New("label: --fixed-in applies to a confirmed finding only")
	}
	if *reportTo == "" {
		return errors.New("label: no server: set --report-to or SLOP_REVIEW_REPORT_URL")
	}
	reportTokenCommand := fs.Lookup("report-token-command").Value.String()
	staticToken := os.Getenv("SLOP_REVIEW_REPORT_TOKEN")
	if note := config.TokenCommandIgnoredNote(staticToken, reportTokenCommand); note != "" {
		_, _ = fmt.Fprintln(stderr, "slop-review: "+note)
	}
	tokens, err := config.ReportTokenSource(staticToken, reportTokenCommand)
	if err != nil {
		return fmt.Errorf("label: %w", err)
	}
	if tokens == nil {
		return errors.New("label: neither SLOP_REVIEW_REPORT_TOKEN nor SLOP_REVIEW_REPORT_TOKEN_COMMAND is set")
	}

	// No spool: labelling posts nothing worth retrying later.
	rep, err := reporter.New(*reportTo, "", "", logHandler, reporter.WithTokenSource(tokens))
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "slop-review: label not recorded: "+err.Error())
		return nil
	}
	if err := rep.Label(ctx, findingID, &reporter.LabelRequest{Verdict: *verdict, Reason: *reason, FixedIn: *fixedIn}); err != nil {
		_, _ = fmt.Fprintln(stderr, "slop-review: label not recorded: "+err.Error())
		return nil
	}
	_, _ = fmt.Fprintf(stdout, "labelled %s %s\n", findingID, *verdict)
	return nil
}
