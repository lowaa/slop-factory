package landing

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/settings"
)

// Candidate is a row waiting to land, with the paths of its ticket.
type Candidate struct {
	Row *LandingRow
	// Paths are the ticket's scopes, relative to the workspace root.
	Paths []string
}

// Candidates lists the rows waiting to land that have not begun, in the order
// Head takes them. A row that has begun is not a candidate: it is a landing in
// hand, which is resumed and not batched.
func (q *PostgresQueue) Candidates(ctx context.Context) ([]Candidate, error) {
	minutes, err := settings.IntIn(ctx, q.db, settings.IntegrationLockPriorityAgingMinutes)
	if err != nil {
		return nil, fmt.Errorf("landing: aging setting: %w", err)
	}
	rows, err := q.db.Query(ctx, `
SELECT w.ticket_id, w.attempt_number, w.change_url, w.head_sha, w.queue_state, w.keeps_place, w.enqueued_at,
       coalesce((SELECT array_agg(p.path ORDER BY p.path) FROM ticket_paths p WHERE p.ticket_id = w.ticket_id), '{}')
  FROM (SELECT *, updated_at AS queued_at FROM landings) w
  JOIN tickets t ON t.id = w.ticket_id
 WHERE w.queue_state = 'queued' AND NOT (w.ticket_id = ANY($1))
 ORDER BY `+headOrderSQL, []int64{}, (time.Duration(minutes) * time.Minute).Seconds())
	if err != nil {
		return nil, fmt.Errorf("landing: candidates: %w", err)
	}
	defer rows.Close()
	var out []Candidate
	for rows.Next() {
		r := &LandingRow{}
		var phase string
		var paths []string
		if err := rows.Scan(&r.TicketID, &r.AttemptNumber, &r.ChangeURL, &r.HeadSHA, &phase, &r.KeepsPlace, &r.EnqueuedAt, &paths); err != nil {
			return nil, fmt.Errorf("landing: scan candidate: %w", err)
		}
		r.setPhase(Phase(phase))
		out = append(out, Candidate{Row: r, Paths: paths})
	}
	return out, rows.Err()
}

// BatchPolicy is what the repo says about which changes may share a batch.
type BatchPolicy interface {
	// App is the app a ticket's paths belong to; false when they belong to
	// none or to several, which makes the change a batch of one.
	App(paths []string) (string, bool)
	// Solo is whether a changed file (relative to the repo root) is one the
	// tests of other changes depend on, or the gate reads: CI's definition,
	// the test harness, settings. A change touching one is a batch of one,
	// because one green run on the batch is evidence for an earlier member only
	// while a later one cannot change how it was tested.
	Solo(file string) bool
	// Migration is the migrations directory a changed file (relative to the
	// repo root) adds to, or false. Two changes adding to the same directory
	// both take the next number and both write its LATEST, so they collide
	// when merged into one batch branch; they need the renumber a serial
	// landing gives the later one.
	Migration(file string) (string, bool)
}

// PathPolicy is a BatchPolicy over path prefixes.
type PathPolicy struct {
	// Apps are the prefixes of the apps' directories in the workspace, as
	// ticket paths are written; an app is independent of the others.
	Apps []string
	// SoloPrefixes are repo-relative prefixes, or exact files, that are solo.
	SoloPrefixes []string
	// MigrationPrefixes are repo-relative migrations directories.
	MigrationPrefixes []string
}

func (p *PathPolicy) App(paths []string) (string, bool) {
	app := ""
	for _, path := range paths {
		found := ""
		for _, a := range p.Apps {
			if strings.HasPrefix(path, a) && len(a) > len(found) {
				found = a
			}
		}
		if found == "" || (app != "" && app != found) {
			return "", false
		}
		app = found
	}
	return app, app != ""
}

func (p *PathPolicy) Solo(file string) bool {
	for _, s := range p.SoloPrefixes {
		if strings.HasPrefix(file, s) {
			return true
		}
	}
	return false
}

func (p *PathPolicy) Migration(file string) (string, bool) {
	for _, m := range p.MigrationPrefixes {
		if strings.HasPrefix(file, m) {
			return m, true
		}
	}
	return "", false
}

// BatchPolicySource gives the policy of the tenant in the context, which a
// repo's declared apps can change between batches.
type BatchPolicySource interface {
	Policy(ctx context.Context) (BatchPolicy, error)
}

type staticPolicy struct{ BatchPolicy }

func (p staticPolicy) Policy(context.Context) (BatchPolicy, error) { return p.BatchPolicy, nil }

// BatchSelector picks the members of the next batch.
type BatchSelector struct {
	policies BatchPolicySource
	files    ChangedFiles
}

// ChangedFiles reads the files a candidate's change touches.
type ChangedFiles interface {
	ChangedFiles(ctx context.Context, row *LandingRow) ([]string, error)
}

func NewBatchSelector(policy BatchPolicy, files ChangedFiles) *BatchSelector {
	return &BatchSelector{policies: staticPolicy{policy}, files: files}
}

// NewBatchSelectorFrom is a selector whose policy is read at each selection.
func NewBatchSelectorFrom(policies BatchPolicySource, files ChangedFiles) *BatchSelector {
	return &BatchSelector{policies: policies, files: files}
}

// Select takes the first candidate and then, in the order given, those of the
// same app up to size in all, skipping one that adds to a migrations directory
// an earlier pick adds to. The first is always taken: a batch of one is the
// serial landing. A candidate that touches a solo file, that is in no single
// app or whose files cannot be read is left for a batch of its own; the head
// whose files cannot be read is an error, because nothing is known to be safe.
//
// It does not rely on ticket scopes being disjoint: with a tenant's scope
// locking off, members may overlap, and BatchBuilder finds the clash when it
// merges them, handing the member back with a conflict like any other.
func (s *BatchSelector) Select(ctx context.Context, cands []Candidate, size int) ([]Candidate, error) {
	if len(cands) == 0 {
		return nil, nil
	}
	policy, err := s.policies.Policy(ctx)
	if err != nil {
		return nil, fmt.Errorf("landing: select: batch policy: %w", err)
	}
	head := cands[0]
	alone, headDirs, err := s.alone(ctx, policy, head)
	if err != nil {
		return nil, fmt.Errorf("landing: select: %w", err)
	}
	if alone || size <= 1 {
		return []Candidate{head}, nil
	}
	app, _ := policy.App(head.Paths)
	picked := []Candidate{head}
	taken := headDirs
	for _, c := range cands[1:] {
		if len(picked) >= size {
			break
		}
		if a, ok := policy.App(c.Paths); !ok || a != app {
			continue
		}
		alone, dirs, err := s.alone(ctx, policy, c)
		if err != nil || alone || intersects(taken, dirs) {
			continue
		}
		for d := range dirs {
			taken[d] = true
		}
		picked = append(picked, c)
	}
	return picked, nil
}

// alone is whether the candidate can only be a batch of one. It also gives
// the migrations directories the change adds to.
func (s *BatchSelector) alone(ctx context.Context, policy BatchPolicy, c Candidate) (bool, map[string]bool, error) {
	dirs := map[string]bool{}
	if _, ok := policy.App(c.Paths); !ok {
		return true, dirs, nil
	}
	files, err := s.files.ChangedFiles(ctx, c.Row)
	if err != nil {
		return true, dirs, err
	}
	for _, f := range files {
		if policy.Solo(f) {
			return true, dirs, nil
		}
		if d, ok := policy.Migration(f); ok {
			dirs[d] = true
		}
	}
	return false, dirs, nil
}

func intersects(a, b map[string]bool) bool {
	for k := range b {
		if a[k] {
			return true
		}
	}
	return false
}
