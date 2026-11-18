package change

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/scope"
)

// Decisions of the independent-apps rule, as the audit and the
// merge_drift_decisions table name them.
const (
	driftWouldAccept = "would_accept"
	driftWouldRefuse = "would_refuse"
)

// DriftDecision is one row of merge_drift_decisions: what the rule said about
// a head behind the base's tip.
type DriftDecision struct {
	TicketID, ChangeID int64
	Decision, Reason   string
	// Dirs are the independent apps the drift touched.
	Dirs    []string
	HeadSHA string
	// TipSHA is the base's tip the drift was read up to; empty when it could
	// not be read.
	TipSHA   string
	Enforced bool
}

func (d *DriftDecision) accepts() bool { return d.Decision == driftWouldAccept }

// audit is the decision as attributes of a merge audit record.
func (d *DriftDecision) audit() []any {
	return []any{"independent_apps", d.Decision, "independent_apps_reason", d.Reason,
		"independent_apps_dirs", strings.Join(d.Dirs, ","), "independent_apps_tip_sha", d.TipSHA,
		"independent_apps_enforced", d.Enforced}
}

// driftRule is the independent-apps rule's inputs: what the repo's manifest
// entry and the ticket say. Every path in it is relative to the repo's root
// except TicketPaths, which are the ticket's own (workspace-relative).
type driftRule struct {
	RepoPath    string
	Apps        []string
	Shared      []scope.SharedPathRule
	DeployOnly  []scope.DeployOnlyRule
	TicketPaths []string
}

// decideDrift is the rule for a head that lags the base only by commits to
// other independent apps: it accepts when every file the base changed since
// the head's merge base is outside the ticket's paths and every shared path,
// and inside one of the repo's independent apps that is not one the ticket's
// paths lie in (same-app code may interact, so tests of the head prove nothing
// about it). Libraries, root files and CI
// directories are not on the list, so they refuse; an empty list never
// accepts. Files that a deploy-only rule takes cleanly (ours is the change's
// own files) are deploy drift, which is not this rule's to judge.
//
// A rename names only where the file went, so what it left is unknown, and
// the rule cannot say it touched no one else's files: it refuses.
//
// The returned decision has Decision, Reason and Dirs set.
func decideDrift(r *driftRule, drift []codehost.FileDiff, ours map[string]bool) DriftDecision {
	dirs := map[string]bool{}
	var why string
	refuse := func(format string, args ...any) {
		if why == "" {
			why = fmt.Sprintf(format, args...)
		}
	}
	if len(r.Apps) == 0 {
		refuse("the repo declares no independent apps")
	}
	own := ticketApps(r)
	shared := scope.NewSharedPaths([]scope.WorkspaceRepo{{Path: r.RepoPath, Shared: r.Shared}})
	for i := range drift {
		f := &drift[i]
		if !f.Status.DescribedByPatch() {
			refuse("%s was renamed, copied or changed in mode, and what it left is unknown", f.Path)
			continue
		}
		if deployOnlyFile(f, r.DeployOnly, ours) == "" {
			continue
		}
		if p, hit := touchedScope(r.TicketPaths, r.RepoPath, f.Path); hit {
			refuse("%s is under the ticket's path %s", f.Path, p)
		}
		if _, hit := shared.Covers(r.RepoPath + "/" + f.Path); hit {
			refuse("%s is a shared path", f.Path)
		}
		app := appOf(r.Apps, f.Path)
		if app == "" {
			refuse("%s is not in an independent app", f.Path)
			continue
		}
		// Not just app: apps may nest, and appOf names only the first match.
		for _, o := range slices.Sorted(maps.Keys(own)) {
			if strings.HasPrefix(f.Path, o) {
				refuse("%s is in this ticket's own app %s", f.Path, o)
				break
			}
		}
		dirs[app] = true
	}
	if why == "" && len(dirs) == 0 {
		// Nothing to vouch for: an empty or truncated read is not a drift in
		// someone else's app.
		refuse("the base's changes name no file in an independent app")
	}
	d := DriftDecision{Decision: driftWouldAccept, Reason: "the base moved only in independent apps", Dirs: sortedKeys(dirs)}
	if why != "" {
		d.Decision, d.Reason = driftWouldRefuse, why
	}
	return d
}

// ticketApps are the independent apps the ticket's paths lie in. A path
// outside every app (a library, the root, dev-tools/) adds none.
func ticketApps(r *driftRule) map[string]bool {
	own := map[string]bool{}
	prefix := r.RepoPath + "/"
	for _, p := range r.TicketPaths {
		rel, ok := strings.CutPrefix(p, prefix)
		if !ok {
			continue
		}
		// A directory listed without its slash still lies in its app.
		if app := appOf(r.Apps, strings.TrimSuffix(rel, "/")+"/"); app != "" {
			own[app] = true
		}
	}
	return own
}

// touchedScope is the ticket path a repo-relative file lies in, or is.
func touchedScope(ticketPaths []string, repoPath, file string) (string, bool) {
	ws := repoPath + "/" + file
	for _, p := range ticketPaths {
		if scope.Conflicts(p, ws) {
			return p, true
		}
	}
	return "", false
}

func appOf(apps []string, file string) string {
	for _, a := range apps {
		if strings.HasPrefix(file, a) {
			return a
		}
	}
	return ""
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// independentApps runs the rule over a head that is behind the base and that
// deploy-only did not take, writes the decision down and returns it. It never
// returns an error: it only records, and a read that fails is a decision to
// refuse. Whether anything is done with an accept is the caller's.
func (s *Service) independentApps(ctx context.Context, tenant, agentID uuid.UUID, changes codehost.Changes, rec *Record, tgt *target, ticketPaths []string, headSHA string, exactHead bool) *DriftDecision {
	d := s.readDrift(ctx, tenant, agentID, changes, rec, tgt, ticketPaths, headSHA, exactHead)
	d.TicketID, d.ChangeID, d.HeadSHA, d.Enforced = rec.TicketID, rec.ID, headSHA, tgt.skipRetestAcrossApps
	if err := s.store.RecordDriftDecision(ctx, &d); err != nil {
		s.logger.Error("change: record drift decision", "ticket", rec.TicketID, "err", err)
	}
	return &d
}

func (s *Service) readDrift(ctx context.Context, tenant, agentID uuid.UUID, changes codehost.Changes, rec *Record, tgt *target, ticketPaths []string, headSHA string, exactHead bool) DriftDecision {
	refuse := func(tip, format string, args ...any) DriftDecision {
		return DriftDecision{Decision: driftWouldRefuse, Reason: fmt.Sprintf(format, args...), TipSHA: tip}
	}
	url := s.hosts.ChangeURL(rec.Change)
	failed := func(op string, err error) {
		s.record(ctx, audit.ChangeOperationFailed, s.attrs(tenant, agentID, rec.TicketID, op, "change", url, "head_sha", headSHA, "err", err.Error())...)
	}
	// The approval covers a head the human was not shown only by its diff
	// against the tip, which a head behind the tip cannot be compared by.
	if !exactHead {
		return refuse("", "the approval covers this head only by its diff against the base's tip")
	}
	diff, err := changes.Diff(ctx, tenant, rec.Change, headSHA)
	if err != nil {
		failed("merge_independent_apps", err)
		return refuse("", "the change's own files could not be read: %v", err)
	}
	if diff.Base != tgt.base || diff.ContainsTip() {
		return refuse(diff.BaseSHA, "the base changed while it was compared")
	}
	base, err := changes.CompareCommits(ctx, tenant, rec.Change, diff.MergeBaseSHA, diff.BaseSHA)
	if err != nil {
		failed("merge_independent_apps", err)
		return refuse(diff.BaseSHA, "what the base changed since %s could not be read: %v", diff.MergeBaseSHA, err)
	}
	if base.BaseSHA != diff.MergeBaseSHA || base.MergeBaseSHA != diff.MergeBaseSHA {
		return refuse(diff.BaseSHA, "the base's history since %s could not be compared", diff.MergeBaseSHA)
	}
	ours := make(map[string]bool, len(diff.Files))
	for _, f := range diff.Files {
		ours[f.Path] = true
	}
	d := decideDrift(&driftRule{RepoPath: tgt.repoPath, Apps: tgt.independentApps, Shared: tgt.shared, DeployOnly: tgt.deployOnly, TicketPaths: ticketPaths}, base.Files, ours)
	d.TipSHA = diff.BaseSHA
	return d
}

// linkDriftOutcome records, on the decisions made for an earlier head of the
// change, whether the checks passed on this one once they have concluded.
// Pending or absent checks conclude nothing. It only records: a failure is
// logged.
func (s *Service) linkDriftOutcome(ctx context.Context, tenant uuid.UUID, changes codehost.Changes, rec *Record, checks *codehost.Checks) {
	if checks.HeadSHA == "" || (checks.State != codehost.CheckSuccess && checks.State != codehost.CheckFailure) {
		return
	}
	// Only the head that took the base in says anything about the drift: a
	// later push that still lags it would be judged by CI that never saw it.
	pending, err := s.store.PendingDriftDecisions(ctx, rec.ID, checks.HeadSHA)
	if err != nil || pending == 0 {
		if err != nil {
			s.logger.Error("change: count drift decisions", "ticket", rec.TicketID, "err", err)
		}
		return
	}
	cmp, err := changes.CompareBase(ctx, tenant, rec.Change, checks.HeadSHA)
	if err != nil || !cmp.ContainsTip || cmp.Base != rec.Base {
		if err != nil {
			s.logger.Warn("change: compare head with base for drift outcome", "ticket", rec.TicketID, "err", err)
		}
		return
	}
	n, err := s.store.LinkDriftOutcome(ctx, rec.ID, checks.HeadSHA, checks.Passed())
	if err != nil {
		s.logger.Error("change: link drift outcome", "ticket", rec.TicketID, "err", err)
		return
	}
	if n > 0 {
		s.logger.Info("change: linked checks to drift decisions", "ticket", rec.TicketID, "head_sha", checks.HeadSHA, "ci_passed", checks.Passed(), "decisions", n)
	}
}
