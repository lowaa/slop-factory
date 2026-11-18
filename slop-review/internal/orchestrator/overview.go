package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lowaa/slop-factory/slop-review/internal/chunk"
	"github.com/lowaa/slop-factory/slop-review/internal/gitctx"
	"github.com/lowaa/slop-factory/slop-review/internal/persona"
	"github.com/lowaa/slop-factory/slop-review/internal/review"
)

// splitStages separates the overview-stage personas from the rest.
func splitStages(personas []*persona.Persona) (overview, rest []*persona.Persona) {
	for _, p := range personas {
		if p.Stage == persona.StageOverview {
			overview = append(overview, p)
		} else {
			rest = append(rest, p)
		}
	}
	return overview, rest
}

// overviewStage is the result of the overview stage: the sessions it ran
// (none for a cache hit) and the text every later session's prompt carries.
type overviewStage struct {
	jobs     []job
	outcomes []outcome
	text     string
}

// runOverview runs the overview-stage personas over the whole change
// (fullScope, whatever directory the review was pointed at) before any
// other session starts. explicit are overview personas the caller asked
// for by name: they always run, and refresh the cache. implicit are the
// library's other overview personas, which every review wants the result
// of: they run only when no cached result matches this diff.
func (o *Orchestrator) runOverview(ctx context.Context, rt *routing, fullScope *gitctx.Scope, explicit, implicit []*persona.Persona) overviewStage {
	var stage overviewStage
	cached := map[string]*review.Review{}
	for _, p := range implicit {
		if r := o.loadCachedOverview(fullScope, p); r != nil {
			cached[p.Name] = r
			continue
		}
		explicit = append(explicit, p)
	}

	all := chunk.Whole(fullScope.Files)
	for _, p := range explicit {
		stage.jobs = append(stage.jobs, job{persona: p, part: part{index: 1, of: 1, owned: all[0].Files}})
	}
	stage.outcomes = o.runAll(ctx, rt, fullScope, stage.jobs)

	byName := cached
	for i := range stage.jobs {
		j, out := &stage.jobs[i], &stage.outcomes[i]
		if out.err != nil {
			continue
		}
		byName[j.persona.Name] = &out.review
		if !out.review.Incomplete {
			o.storeOverview(fullScope, j.persona, &out.review)
		}
	}

	var sections []string
	for _, p := range append(append([]*persona.Persona{}, explicit...), implicit...) {
		if r := byName[p.Name]; r != nil {
			sections = append(sections, renderOverview(p.Name, r))
			delete(byName, p.Name)
		}
	}
	stage.text = strings.Join(sections, "\n")
	return stage
}

func renderOverview(name string, r *review.Review) string {
	var b strings.Builder
	fmt.Fprintf(&b, "### %s\n%s\n", name, strings.TrimSpace(r.Summary))
	if len(r.Findings) > 0 {
		b.WriteString("Confirmed breaks it found:\n")
		for _, f := range r.Findings {
			loc := f.File
			if f.Line > 0 {
				loc = fmt.Sprintf("%s:%d", f.File, f.Line)
			}
			fmt.Fprintf(&b, "- [%s] %s: %s\n", f.Severity, loc, f.Summary)
		}
	}
	return b.String()
}

// overviewKey identifies one persona's overview of one diff. The persona's
// prompt is part of it, so editing the persona doesn't serve a stale map;
// so are the intent, commit log and guidance, which the overview's prompt
// carries; so is the repository, since the cache directory is shared by every repo.
func overviewKey(fullScope *gitctx.Scope, intent string, p *persona.Persona) string {
	h := sha256.New()
	for _, part := range []string{p.Name, p.Prompt, fullScope.RepoRoot, fullScope.BaseRef, fullScope.Diff(), fullScope.CommitLog, intent, guidance} {
		fmt.Fprintf(h, "%d:%s", len(part), part)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (o *Orchestrator) overviewPath(fullScope *gitctx.Scope, p *persona.Persona) string {
	if o.opts.OverviewCacheDir == "" {
		return ""
	}
	return filepath.Join(o.opts.OverviewCacheDir, overviewKey(fullScope, o.opts.Intent, p)+".json")
}

// loadCachedOverview returns the cached overview for this diff, or nil. A
// cache that can't be read is a miss, never an error: it only saves a run.
func (o *Orchestrator) loadCachedOverview(fullScope *gitctx.Scope, p *persona.Persona) *review.Review {
	path := o.overviewPath(fullScope, p)
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var r review.Review
	if json.Unmarshal(data, &r) != nil || r.Validate() != nil {
		return nil
	}
	o.logger.Info("reusing cached overview", "persona", p.Name)
	return &r
}

func (o *Orchestrator) storeOverview(fullScope *gitctx.Scope, p *persona.Persona, r *review.Review) {
	path := o.overviewPath(fullScope, p)
	if path == "" {
		return
	}
	data, err := json.Marshal(r)
	if err == nil {
		err = os.MkdirAll(filepath.Dir(path), 0o755)
	}
	if err == nil {
		// Renamed into place: a concurrent run must never read half a file.
		var tmp *os.File
		if tmp, err = os.CreateTemp(filepath.Dir(path), "overview-*.tmp"); err == nil {
			_, err = tmp.Write(data)
			err = joinErr(err, tmp.Close())
			if err == nil {
				err = os.Rename(tmp.Name(), path)
			}
			if err != nil {
				_ = os.Remove(tmp.Name())
			}
		}
	}
	if err != nil {
		o.logger.Warn("could not cache overview", "persona", p.Name, "err", err)
	}
}

func joinErr(a, b error) error {
	if a != nil {
		return a
	}
	return b
}
