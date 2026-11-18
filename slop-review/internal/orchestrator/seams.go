package orchestrator

import (
	"context"
	_ "embed"
	"fmt"
	"strings"

	"github.com/lowaa/slop-factory/slop-review/internal/gitctx"
	"github.com/lowaa/slop-factory/slop-review/internal/persona"
)

//go:embed seams.md
var seamsPrompt string

// seamsPersona is internal to the orchestrator rather than a file in the
// persona library: it only makes sense after a split review, fed the other
// sessions' output, so offering it as a standalone --persona would invite
// running it where it can only produce noise.
var seamsPersona = &persona.Persona{Name: SeamsPersona, Prompt: seamsPrompt, Chunking: persona.ChunkWhole}

// runSeamPass runs one session over the boundaries between chunks, given
// what every chunk session concluded. It owns no files -- it answers for
// the gaps between them -- so it never warns about files it didn't open.
func (o *Orchestrator) runSeamPass(ctx context.Context, rt *routing, scope *gitctx.Scope, jobs []job, outcomes []outcome) (job, outcome) {
	j := job{persona: seamsPersona, part: part{index: 1, of: 1}}
	return j, o.runSession(ctx, rt, scope, &j, seamsPrompt, buildSeamPrompt(scope, o.opts.Intent, jobs, outcomes), nil)
}

func buildSeamPrompt(scope *gitctx.Scope, intent string, jobs []job, outcomes []outcome) string {
	var b strings.Builder
	writeHeader(&b, scope, intent)
	b.WriteString("\n")
	writeManifest(&b, scope, nil)

	b.WriteString("\nHow the change was split, and what each part's reviewer reported:\n")
	for i := range jobs {
		j, out := &jobs[i], &outcomes[i]
		if j.part.of <= 1 {
			// A persona that reviewed the whole change saw every seam
			// itself; its findings are still worth not repeating.
			fmt.Fprintf(&b, "\n## %s (whole change)\n", j.persona.Name)
		} else {
			fmt.Fprintf(&b, "\n## %s, part %d of %d: %s\n", j.persona.Name, j.part.index, j.part.of, strings.Join(j.part.owned, ", "))
		}
		if out.err != nil {
			fmt.Fprintf(&b, "This session failed, so its files went unreviewed: %v\n", out.err)
			continue
		}
		fmt.Fprintf(&b, "Verdict: %s. %s\n", out.review.Verdict, out.review.Summary)
		for _, f := range out.review.Findings {
			loc := f.File
			if f.Line > 0 {
				loc = fmt.Sprintf("%s:%d", f.File, f.Line)
			}
			fmt.Fprintf(&b, "- [%s] %s: %s\n", f.Severity, loc, f.Summary)
		}
	}
	return b.String()
}
