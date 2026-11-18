package steward

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

// Subject is what a proposal is about, as the steward saw it when it made
// the proposal. Checks compare it to what the server reads now.
type Subject struct {
	TicketID int64
	// Revision is the ticket's revision at the steward's assessment.
	Revision int
	// HeadSHA is the head of the ticket's change at the assessment; empty
	// when the category does not concern a change.
	HeadSHA string
	// ProposedPaths are the paths an edit would leave the ticket with.
	ProposedPaths []string
	// EditedFields are the fields an edit proposal changes.
	EditedFields []string
	// Body is the text a discussion answer would post as the owner.
	Body string
	// Category is the category being checked, set by the engine.
	Category string
}

// Evidence is one check's outcome. Ref points at where the fact was read
// (a CI run, a comment) so the owner can see it for themselves.
type Evidence struct {
	Check  string `json:"check"`
	OK     bool   `json:"ok"`
	Ref    string `json:"ref,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// Check is one server-side verification. An error means the fact could not
// be read, which the engine treats as a failed check.
type Check interface {
	Name() string
	Run(ctx context.Context, s *Subject) (Evidence, error)
}

// The readers are the server's own sources of fact, narrowed to what a check
// asks, so each check is tested with a fake and wired to the real service
// where the engine is built.
type (
	// CIReader says whether every check passed on a change's head.
	CIReader interface {
		ChecksPassed(ctx context.Context, ticketID int64, headSHA string) (passed bool, ref string, err error)
	}
	// DiffReader lists the paths the ticket's change touches.
	DiffReader interface {
		ChangedPaths(ctx context.Context, ticketID int64) ([]string, error)
	}
	// TicketReader reads the ticket as it is now.
	TicketReader interface {
		Revision(ctx context.Context, ticketID int64) (int, error)
		Paths(ctx context.Context, ticketID int64) ([]string, error)
		Status(ctx context.Context, ticketID int64) (string, error)
	}
	// PlanReader says whether the ticket has a plan a person approved.
	PlanReader interface {
		ApprovedPlan(ctx context.Context, ticketID int64) (approved bool, ref string, err error)
	}
	// HeadReader gives the current head of the ticket's change.
	HeadReader interface {
		CurrentHead(ctx context.Context, ticketID int64) (string, error)
	}
	// MarkerReader says whether the ticket's open discussion is one the
	// briefing reserves for a human.
	MarkerReader interface {
		HumanOnly(ctx context.Context, ticketID int64) (humanOnly bool, ref string, err error)
	}
)

// StatusPreparing is the ticket status open_ticket requires, in the words of
// ticket.StatusPreparing.
const StatusPreparing = "preparing"

type checkFunc struct {
	name string
	run  func(ctx context.Context, s *Subject) (Evidence, error)
}

func (c checkFunc) Name() string { return c.name }
func (c checkFunc) Run(ctx context.Context, s *Subject) (Evidence, error) {
	ev, err := c.run(ctx, s)
	ev.Check = c.name
	return ev, err
}

// EditFieldsAllowed holds when an edit proposal changes nothing but the
// fields its category covers. The other checks look at facts (revision,
// paths), so without this a proposal could be labelled with a narrow category
// and carry a wider change, which the owner would then not have seen.
func EditFieldsAllowed() Check {
	return checkFunc{CheckEditFieldsAllowed, func(_ context.Context, s *Subject) (Evidence, error) {
		cat, ok := Lookup(s.Category)
		if !ok {
			return Evidence{Detail: "unknown category"}, nil
		}
		var extra []string
		for _, f := range s.EditedFields {
			if !slices.Contains(cat.EditFields, f) {
				extra = append(extra, f)
			}
		}
		if len(extra) > 0 {
			return Evidence{Detail: cat.ID + " does not cover changing: " + strings.Join(extra, ", ")}, nil
		}
		return Evidence{OK: true}, nil
	}}
}

func CIGreenOnHead(r CIReader) Check {
	return checkFunc{CheckCIGreenOnHead, func(ctx context.Context, s *Subject) (Evidence, error) {
		if s.HeadSHA == "" {
			return Evidence{Detail: "no head to check"}, nil
		}
		ok, ref, err := r.ChecksPassed(ctx, s.TicketID, s.HeadSHA)
		if err != nil {
			return Evidence{}, err
		}
		return Evidence{OK: ok, Ref: ref, Detail: fmt.Sprintf("checks on %s", short(s.HeadSHA))}, nil
	}}
}

func HeadUnchanged(r HeadReader) Check {
	return checkFunc{CheckHeadUnchanged, func(ctx context.Context, s *Subject) (Evidence, error) {
		now, err := r.CurrentHead(ctx, s.TicketID)
		if err != nil {
			return Evidence{}, err
		}
		if s.HeadSHA == "" || now != s.HeadSHA {
			return Evidence{Detail: fmt.Sprintf("assessed %s, now %s", short(s.HeadSHA), short(now))}, nil
		}
		return Evidence{OK: true, Ref: now}, nil
	}}
}

func TicketRevUnchanged(r TicketReader) Check {
	return checkFunc{CheckTicketRevUnchanged, func(ctx context.Context, s *Subject) (Evidence, error) {
		now, err := r.Revision(ctx, s.TicketID)
		if err != nil {
			return Evidence{}, err
		}
		if now != s.Revision {
			return Evidence{Detail: fmt.Sprintf("assessed at revision %d, now %d", s.Revision, now)}, nil
		}
		return Evidence{OK: true, Ref: fmt.Sprintf("revision %d", now)}, nil
	}}
}

func DiffWithinScope(d DiffReader, t TicketReader) Check {
	return checkFunc{CheckDiffWithinScope, func(ctx context.Context, s *Subject) (Evidence, error) {
		changed, err := d.ChangedPaths(ctx, s.TicketID)
		if err != nil {
			return Evidence{}, err
		}
		scope, err := t.Paths(ctx, s.TicketID)
		if err != nil {
			return Evidence{}, err
		}
		if len(changed) == 0 {
			return Evidence{Detail: "the change touches no paths"}, nil
		}
		var outside []string
		for _, p := range changed {
			if !coveredBy(p, scope) {
				outside = append(outside, p)
			}
		}
		if len(outside) > 0 {
			return Evidence{Detail: "outside the ticket's scope: " + strings.Join(outside, ", ")}, nil
		}
		return Evidence{OK: true, Detail: fmt.Sprintf("%d changed paths, all in scope", len(changed))}, nil
	}}
}

// NoNewPaths holds when the paths an edit proposes are all already covered by
// the ticket's: the proposal narrows or keeps the scope, it never claims more.
func NoNewPaths(t TicketReader) Check {
	return checkFunc{CheckNoNewPaths, func(ctx context.Context, s *Subject) (Evidence, error) {
		current, err := t.Paths(ctx, s.TicketID)
		if err != nil {
			return Evidence{}, err
		}
		var added []string
		for _, p := range s.ProposedPaths {
			if !coveredBy(p, current) {
				added = append(added, p)
			}
		}
		if len(added) > 0 {
			return Evidence{Detail: "adds paths: " + strings.Join(added, ", ")}, nil
		}
		return Evidence{OK: true}, nil
	}}
}

// TicketPreparing holds while the ticket is still in preparing, read now.
func TicketPreparing(r TicketReader) Check {
	return checkFunc{CheckTicketPreparing, func(ctx context.Context, s *Subject) (Evidence, error) {
		status, err := r.Status(ctx, s.TicketID)
		if err != nil {
			return Evidence{}, err
		}
		if status != StatusPreparing {
			return Evidence{Detail: fmt.Sprintf("ticket is %q, not preparing", status)}, nil
		}
		return Evidence{OK: true, Ref: "status " + status}, nil
	}}
}

// ApprovedPlan holds when a person has approved a plan on the ticket, so the
// bookkeeping it leads to is carrying out their decision.
func ApprovedPlan(r PlanReader) Check {
	return checkFunc{CheckApprovedPlan, func(ctx context.Context, s *Subject) (Evidence, error) {
		ok, ref, err := r.ApprovedPlan(ctx, s.TicketID)
		if err != nil {
			return Evidence{}, err
		}
		if !ok {
			return Evidence{Detail: "no approved plan on the ticket"}, nil
		}
		return Evidence{OK: true, Ref: ref}, nil
	}}
}

func NoHumanOnlyMarker(r MarkerReader) Check {
	return checkFunc{CheckNoHumanOnlyMarker, func(ctx context.Context, s *Subject) (Evidence, error) {
		humanOnly, ref, err := r.HumanOnly(ctx, s.TicketID)
		if err != nil {
			return Evidence{}, err
		}
		if humanOnly {
			return Evidence{Ref: ref, Detail: "this discussion is reserved for a human"}, nil
		}
		return Evidence{OK: true, Ref: ref}, nil
	}}
}

// coveredBy reports whether path is one of scope or inside a directory
// (written with a trailing slash) of it.
func coveredBy(path string, scope []string) bool {
	return slices.ContainsFunc(scope, func(s string) bool {
		if s == path {
			return true
		}
		return strings.HasSuffix(s, "/") && strings.HasPrefix(path, s)
	})
}

func short(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}
