package attemptsignals

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/attemptsignals/regressions"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/change"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// ChangeHosts finds the code host client for a change's host.
type ChangeHosts interface {
	Changes(host string) (codehost.Changes, bool)
}

// HostSource reads an attempt's inputs from its recorded change and the code
// host. It supplies what the host can show of the head: the diff and whether
// its checks passed. Failed test names, CI reruns and the own-test results at
// the base are not recorded anywhere yet, so those stay unknown.
type HostSource struct {
	db      *db.DB
	changes change.Store
	hosts   ChangeHosts
}

func NewHostSource(database *db.DB, changes change.Store, hosts ChangeHosts) *HostSource {
	return &HostSource{db: database, changes: changes, hosts: hosts}
}

var _ Source = (*HostSource)(nil)

func (s *HostSource) Inputs(ctx context.Context, ref ticket.AttemptRef) (*Inputs, error) {
	tenant, err := db.TenantFrom(ctx)
	if err != nil {
		return nil, err
	}
	var status string
	err = s.db.QueryRow(ctx, `SELECT status FROM attempts WHERE ticket_id = $1 AND number = $2`, ref.TicketID, ref.Number).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoAttempt
	}
	if err != nil {
		return nil, fmt.Errorf("attemptsignals: attempt: %w", err)
	}
	in := &Inputs{Final: status == "closed" || status == "done"}

	rows, err := s.db.Query(ctx, `SELECT path FROM ticket_paths WHERE ticket_id = $1`, ref.TicketID)
	if err != nil {
		return nil, fmt.Errorf("attemptsignals: scopes: %w", err)
	}
	in.Scopes, err = pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("attemptsignals: scopes: %w", err)
	}

	rec, err := s.changes.Current(ctx, ref.TicketID, ref.Number)
	if errors.Is(err, change.ErrNoChange) {
		return in, nil
	}
	if err != nil {
		return nil, err
	}
	in.Final = in.Final || rec.MergedAt != nil || rec.ClosedAt != nil

	host, ok := s.hosts.Changes(rec.Change.Host)
	if !ok {
		return nil, fmt.Errorf("attemptsignals: no code host %q", rec.Change.Host)
	}
	checks, err := host.Checks(ctx, tenant, rec.Change)
	if err != nil {
		return nil, fmt.Errorf("attemptsignals: checks: %w", err)
	}
	in.HeadSHA = checks.HeadSHA
	switch checks.State {
	case codehost.CheckSuccess, codehost.CheckFailure:
		in.CI = &CI{Runs: []regressions.Run{{Pipeline: 1, Passed: checks.Passed()}}}
	}
	diff, err := host.Diff(ctx, tenant, rec.Change, checks.HeadSHA)
	switch {
	case errors.Is(err, codehost.ErrDiffUnsupported):
		// Too large or unparseable: the diff signals stay unknown.
	case err != nil:
		return nil, fmt.Errorf("attemptsignals: diff: %w", err)
	default:
		// An unrenderable diff leaves the diff signals unknown, as an
		// unsupported one does.
		in.Diff, _ = unifiedDiff(diff)
	}
	return in, nil
}

// unifiedDiff renders the host's per-file patches as the git-style diff
// diffcheck reads. It fails closed (false) rather than guess:
//   - a file with no patch other than a removal or pure rename: the host omits
//     the patch of binary and of too-large files alike, and a too-large text
//     file must not read as clean;
//   - a path with a control character, which a repository can contain and
//     which would let a file name forge a header line.
func unifiedDiff(d *codehost.Diff) (string, bool) {
	var b strings.Builder
	for _, f := range d.Files {
		old := f.Path
		if f.PreviousPath != "" {
			old = f.PreviousPath
		}
		if hasControl(f.Path) || hasControl(old) {
			return "", false
		}
		fmt.Fprintf(&b, "diff --git a/%s b/%s\n", old, f.Path)
		switch f.Status {
		case codehost.DiffRemoved:
			b.WriteString("deleted file mode 100644\n")
		case codehost.DiffRenamed:
			fmt.Fprintf(&b, "rename from %s\nrename to %s\n", old, f.Path)
		}
		if f.Patch == "" {
			if f.Status != codehost.DiffRemoved && f.Status != codehost.DiffRenamed {
				return "", false
			}
			continue
		}
		b.WriteString(f.Patch)
		if !strings.HasSuffix(f.Patch, "\n") {
			b.WriteByte('\n')
		}
	}
	return b.String(), true
}

func hasControl(s string) bool { return strings.ContainsFunc(s, unicode.IsControl) }
