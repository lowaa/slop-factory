package stewardwire

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/manifest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/reviewreport"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward/ticketready"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// ReviewRunReader implements steward.SlopReviewReader over the slop-review
// runs filed under the ticket's live attempt (liveAttempt, the one whose
// change the other checks read). Only a supervisor's runs count: it ran the
// review for its agent, while an agent's own report is only its word.
type ReviewRunReader struct {
	runs    reviewreport.HeadRunReader
	tickets ChangeTickets
}

func NewReviewRunReader(runs reviewreport.HeadRunReader, tickets ChangeTickets) *ReviewRunReader {
	return &ReviewRunReader{runs: runs, tickets: tickets}
}

// SlopReviews is, per persona, the newest supervisor-posted completed run of
// the head that covers it. ref names the newest run read.
func (r *ReviewRunReader) SlopReviews(ctx context.Context, ticketID int64, headSHA string) ([]steward.PersonaReview, string, error) {
	attempt, err := liveAttempt(ctx, r.tickets, ticketID)
	if err != nil {
		return nil, "", err
	}
	runs, err := r.runs.RunsForAttemptHead(ctx, ticketID, attempt, headSHA, reviewreport.PostedBySupervisorOnly)
	if err != nil {
		return nil, "", fmt.Errorf("read the review runs of ticket %d attempt %d: %w", ticketID, attempt, err)
	}
	var out []steward.PersonaReview
	seen := map[string]bool{}
	ref := ""
	for i := range runs {
		run := &runs[i]
		for _, persona := range run.Personas {
			if seen[persona] {
				continue
			}
			seen[persona] = true
			out = append(out, steward.PersonaReview{
				Persona:    persona,
				Approved:   run.Verdict == "approve" || run.Verdict == "approve_with_comments",
				Incomplete: run.Incomplete,
				Blocking:   run.Blocking,
			})
			if ref == "" {
				ref = fmt.Sprintf("attempt %d review run %s", attempt, run.RunID)
			}
		}
	}
	return out, ref, nil
}

// ReviewRequestReason implements steward.ReviewMarkReader: the reason an
// agent gave for asking a human to read a change it was free to merge, posted
// as a comment led by ticket.VoluntaryReviewLead during the current review.
func (r *CommentReader) ReviewRequestReason(ctx context.Context, ticketID int64) (string, string, error) {
	single, err := r.singleAttempt(ctx, ticketID)
	if err != nil {
		return "", "", err
	}
	if !single {
		return "", "", fmt.Errorf("ticket %d has several live attempts; whose review request a comment is unknown", ticketID)
	}
	t, err := r.tickets.Get(ctx, ticketID)
	if err != nil {
		return "", "", fmt.Errorf("read ticket %d: %w", ticketID, err)
	}
	comments, err := r.tickets.ListComments(ctx, ticketID)
	if err != nil {
		return "", "", fmt.Errorf("read comments of ticket %d: %w", ticketID, err)
	}
	for i := len(comments) - 1; i >= 0; i-- {
		c := &comments[i]
		if t.ReviewStartedAt != nil && c.CreatedAt.Before(*t.ReviewStartedAt) {
			break
		}
		if c.AuthorAgentID == nil {
			continue
		}
		if reason, ok := strings.CutPrefix(c.Body, ticket.VoluntaryReviewLead); ok {
			return strings.TrimSpace(reason), commentRef(c), nil
		}
	}
	return "", "", nil
}

var (
	generatedHeader = regexp.MustCompile(`(?m)^[+ ]\s*(//|#|/\*|\*|--|<!--)?\s*Code generated .*DO NOT EDIT\.`)
	attrLine        = regexp.MustCompile(`^\+\s*(\S+)\s+.*\blinguist-generated(=true)?\b`)
)

// DiffLines counts a change's lines, from the patch the host gives.
// Generated files are the ones with a "Code generated ... DO NOT EDIT."
// header in their patch, or that a .gitattributes in the same change marks
// linguist-generated. A file whose generated mark is not in the diff is
// counted: that only costs a human look.
type DiffLines struct{ changes *ChangeReader }

func NewDiffLines(c *ChangeReader) *DiffLines { return &DiffLines{changes: c} }

// ChangedLines implements steward.LineReader.
func (d *DiffLines) ChangedLines(ctx context.Context, ticketID int64) (*steward.LineCount, error) {
	checks, rec, host, tenant, err := d.changes.checks(ctx, ticketID)
	if err != nil {
		return nil, err
	}
	diff, err := host.Diff(ctx, tenant, rec.Change, checks.HeadSHA)
	if err != nil {
		return nil, fmt.Errorf("read diff of ticket %d: %w", ticketID, err)
	}
	marked := map[string][]string{}
	for _, f := range diff.Files {
		if path.Base(f.Path) != ".gitattributes" {
			continue
		}
		for _, line := range strings.Split(f.Patch, "\n") {
			if m := attrLine.FindStringSubmatch(line); m != nil {
				marked[path.Dir(f.Path)] = append(marked[path.Dir(f.Path)], m[1])
			}
		}
	}
	var n steward.LineCount
	for _, f := range diff.Files {
		// A file the host gave no patch for (binary, too large) cannot be
		// counted; reading it as zero lines would let it through.
		if f.Patch == "" {
			return nil, fmt.Errorf("the host gave no patch for %s, so its lines cannot be counted", f.Path)
		}
		lines := patchLines(f.Patch)
		if isGenerated(&f, marked) {
			n.Generated += lines
		} else {
			n.Counted += lines
		}
	}
	return &n, nil
}

func patchLines(patch string) int {
	n := 0
	for _, l := range strings.Split(patch, "\n") {
		if strings.HasPrefix(l, "+") || strings.HasPrefix(l, "-") {
			n++
		}
	}
	return n
}

func isGenerated(f *codehost.FileDiff, marked map[string][]string) bool {
	if generatedHeader.MatchString(f.Patch) {
		return true
	}
	for dir, patterns := range marked {
		if dir != "." && !strings.HasPrefix(f.Path, dir+"/") {
			continue
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(f.Path, dir), "/")
		for _, p := range patterns {
			if ok, _ := path.Match(strings.TrimPrefix(p, "/"), rel); ok {
				return true
			}
			if ok, _ := path.Match(p, path.Base(rel)); ok && !strings.Contains(p, "/") {
				return true
			}
		}
	}
	return false
}

// ManifestRepos reads the workspace manifest.
type ManifestRepos interface {
	Get(ctx context.Context) (*manifest.Manifest, error)
}

// ProtectedPaths implements steward.ProtectedPathReader over the manifest's
// protected_paths.
type ProtectedPaths struct{ manifests ManifestRepos }

func NewProtectedPaths(m ManifestRepos) *ProtectedPaths { return &ProtectedPaths{manifests: m} }

// ProtectedAmong tries each path as given and under each repo's path, since
// a host reports paths relative to its repo and a ticket's are relative to the
// workspace; reading either wrongly only costs a human look.
func (p *ProtectedPaths) ProtectedAmong(ctx context.Context, paths []string) ([]string, error) {
	rules, err := p.rules(ctx, paths)
	if err != nil {
		return nil, err
	}
	var hits []string
	seen := map[string]bool{}
	for _, candidate := range paths {
		rule, ok := rules[candidate]
		if !ok || seen[candidate] {
			continue
		}
		seen[candidate] = true
		line := candidate + " (" + rule.Path
		if rule.Reason != "" {
			line += ": " + rule.Reason
		}
		hits = append(hits, line+")")
	}
	return hits, nil
}

// Protected implements ticketready.ProtectedPathReader: the reason of the
// rule covering each protected path, at the manifest's current revision.
func (p *ProtectedPaths) Protected(ctx context.Context, paths []string) (map[string]string, error) {
	rules, err := p.rules(ctx, paths)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rules))
	for path, rule := range rules {
		out[path] = rule.Reason
	}
	return out, nil
}

func (p *ProtectedPaths) rules(ctx context.Context, paths []string) (map[string]*manifest.ProtectedPathRule, error) {
	m, err := p.manifests.Get(ctx)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	out := map[string]*manifest.ProtectedPathRule{}
	if m == nil {
		// No manifest declared yet: no repo has protected paths.
		return out, nil
	}
	for _, candidate := range paths {
		if rule, ok := manifest.Protected(m.Repos, candidate); ok {
			out[candidate] = rule
			continue
		}
		for i := range m.Repos {
			if rule, ok := manifest.Protected(m.Repos, m.Repos[i].Path+"/"+candidate); ok {
				out[candidate] = rule
				break
			}
		}
	}
	return out, nil
}

var (
	_ steward.SlopReviewReader        = (*ReviewRunReader)(nil)
	_ steward.ReviewMarkReader        = (*CommentReader)(nil)
	_ steward.LineReader              = (*DiffLines)(nil)
	_ steward.ProtectedPathReader     = (*ProtectedPaths)(nil)
	_ ticketready.ProtectedPathReader = (*ProtectedPaths)(nil)
)
