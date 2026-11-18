// Package ticketready is the steward's ticket_ready category: the steward has
// read a preparing ticket's brief and proposes opening it. The allowlist
// entry is in steward/category.go and the comparator in steward/agreement,
// since neither can import this package.
package ticketready

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward"
)

// CategoryID is the category's id. It folds in the retired open_ticket: that
// one's stored assessments stay readable under their own id.
const CategoryID = "ticket_ready"

// Tickets is the ticket as the brief checks read it. Revision, Paths and
// Status are the steward's TicketReader.
type Tickets interface {
	steward.TicketReader
	Body(ctx context.Context, ticketID int64) (string, error)
	// Blockers are the ids of the tickets this one is blocked on.
	Blockers(ctx context.Context, ticketID int64) ([]int64, error)
}

// ProtectedPathReader reads the repo's protected_paths rules at their current
// revision, not the assessment's.
type ProtectedPathReader interface {
	// Protected returns, for each of paths that a rule covers, the rule's
	// reason (possibly empty), keyed by path.
	Protected(ctx context.Context, paths []string) (map[string]string, error)
}

type check struct {
	name string
	run  func(ctx context.Context, s *steward.Subject) (steward.Evidence, error)
}

func (c check) Name() string { return c.name }
func (c check) Run(ctx context.Context, s *steward.Subject) (steward.Evidence, error) {
	ev, err := c.run(ctx, s)
	ev.Check = c.name
	return ev, err
}

var (
	doLine    = regexp.MustCompile(`(?im)^\s*(?:[#*_>\-\s]*)do\b[\s*_:]*(?::|$|\s)`)
	testsWord = regexp.MustCompile(`(?i)\btests?\b`)
	// blockerClause finds what follows "blocked on", "depends on" or "waits
	// on", up to the end of the sentence, so "blocked on #12 and #13" names
	// both. Anything else in the clause is read as a blocker too: failing
	// closed costs a human look, missing one starts work early.
	blockerClause = regexp.MustCompile(`(?i)\b(?:blocked\s+(?:on|by)|depends\s+on|waits?\s+(?:on|for))\b([^\n]*?)(?:\.(?:\s|$)|\n|$)`)
	ticketRef     = regexp.MustCompile(`#(\d+)`)
)

// TicketHasBrief holds when the ticket has declared paths, a "Do" and
// mentions its tests; Detail records which of the three it found.
func TicketHasBrief(t Tickets) steward.Check {
	return check{steward.CheckTicketHasBrief, func(ctx context.Context, s *steward.Subject) (steward.Evidence, error) {
		paths, err := t.Paths(ctx, s.TicketID)
		if err != nil {
			return steward.Evidence{}, err
		}
		body, err := t.Body(ctx, s.TicketID)
		if err != nil {
			return steward.Evidence{}, err
		}
		found := map[string]bool{
			"paths": len(paths) > 0,
			"Do":    doLine.MatchString(body),
			"tests": testsWord.MatchString(body),
		}
		var missing []string
		for _, k := range []string{"paths", "Do", "tests"} {
			if !found[k] {
				missing = append(missing, k)
			}
		}
		detail := fmt.Sprintf("paths: %d, Do: %t, tests: %t", len(paths), found["Do"], found["tests"])
		if len(missing) > 0 {
			return steward.Evidence{Detail: detail + "; missing " + strings.Join(missing, ", ")}, nil
		}
		return steward.Evidence{OK: true, Detail: detail}, nil
	}}
}

// BlockersRecorded holds when every ticket the body says it waits on is
// recorded as a blocker, so opening it cannot start work early.
func BlockersRecorded(t Tickets) steward.Check {
	return check{steward.CheckBlockersRecorded, func(ctx context.Context, s *steward.Subject) (steward.Evidence, error) {
		body, err := t.Body(ctx, s.TicketID)
		if err != nil {
			return steward.Evidence{}, err
		}
		recorded, err := t.Blockers(ctx, s.TicketID)
		if err != nil {
			return steward.Evidence{}, err
		}
		var named, unrecorded []string
		for _, clause := range blockerClause.FindAllStringSubmatch(body, -1) {
			for _, m := range ticketRef.FindAllStringSubmatch(clause[1], -1) {
				id := m[1]
				if slices.Contains(named, id) {
					continue
				}
				named = append(named, id)
				if !slices.ContainsFunc(recorded, func(r int64) bool { return fmt.Sprint(r) == id }) {
					unrecorded = append(unrecorded, "#"+id)
				}
			}
		}
		detail := fmt.Sprintf("%d named in the body, %d recorded", len(named), len(recorded))
		if len(unrecorded) > 0 {
			return steward.Evidence{Detail: detail + "; not recorded: " + strings.Join(unrecorded, ", ")}, nil
		}
		return steward.Evidence{OK: true, Detail: detail}, nil
	}}
}

// NoProtectedPath holds when no declared path is protected. A ticket with no
// declared paths fails: the check cannot clear what it cannot read.
func NoProtectedPath(t Tickets, p ProtectedPathReader) steward.Check {
	return check{steward.CheckNoProtectedPath, func(ctx context.Context, s *steward.Subject) (steward.Evidence, error) {
		paths, err := t.Paths(ctx, s.TicketID)
		if err != nil {
			return steward.Evidence{}, err
		}
		if len(paths) == 0 {
			return steward.Evidence{Detail: "the ticket declares no paths, so it counts as protected"}, nil
		}
		hits, err := p.Protected(ctx, paths)
		if err != nil {
			return steward.Evidence{}, err
		}
		if len(hits) > 0 {
			var parts []string
			for _, path := range paths {
				if reason, ok := hits[path]; ok {
					if reason != "" {
						path += " (" + reason + ")"
					}
					parts = append(parts, path)
				}
			}
			return steward.Evidence{Detail: "protected: " + strings.Join(parts, ", ")}, nil
		}
		return steward.Evidence{OK: true, Detail: fmt.Sprintf("%d paths, none protected", len(paths))}, nil
	}}
}
