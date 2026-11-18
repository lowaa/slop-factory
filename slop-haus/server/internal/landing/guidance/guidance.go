package guidance

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
)

// DefaultBound is how many collisions a ticket may be handed back for before
// the server stops re-queueing it. Three: a second collision can be bad luck
// (main moved twice while the author fixed the first), a third says the
// ticket and whatever keeps landing ahead of it are one piece of work, and a
// fourth handback would only be the same loop with the author's time in it.
const DefaultBound = 3

// Landed is a change that went onto the base after a ticket's work began.
type Landed struct {
	TicketID  int64
	ChangeURL string
	MergeSHA  string
	// Files are the paths it touched.
	Files []string
}

// Source is what the server knows about the base branch.
type Source interface {
	// LandedSince lists the changes that landed on the base after the commit
	// the ticket's work began from, oldest first, not counting the ticket's own.
	LandedSince(ctx context.Context, ticketID int64, attempt int) ([]Landed, error)
	// HeadFiles are the paths the change touches at head.
	HeadFiles(ctx context.Context, ticketID int64, attempt int, head string) ([]string, error)
}

// History counts what has already been handed back.
type History interface {
	// Collisions is how many times the ticket was handed back for a collision
	// since a human last looked at the ticket for that reason.
	Collisions(ctx context.Context, ticketID int64) (int, error)
}

// Cause is why a landing collided.
type Cause string

const (
	CauseMergeConflict Cause = "merge_conflict"
	CauseInteraction   Cause = "interaction"
)

// Verdict is what the server can say about a red check after a clean merge of
// the base.
type Verdict int

const (
	// VerdictUnknown: the server could not tell, so it says nothing either way.
	VerdictUnknown Verdict = iota
	// VerdictInteraction: green on the base's tip and on the change's own head,
	// red on the merge, so the failure comes from the pair.
	VerdictInteraction
	// VerdictOwnBug: red on the change's own head before the merge.
	VerdictOwnBug
	// VerdictRedOnBase: red on the base's tip too.
	VerdictRedOnBase
)

// Facts are the answers CI gave about a red check.
type Facts struct {
	GreenOnBase *bool
	GreenOnHead *bool
}

func (f Facts) verdict() Verdict {
	switch {
	case f.GreenOnHead != nil && !*f.GreenOnHead:
		return VerdictOwnBug
	case f.GreenOnBase != nil && !*f.GreenOnBase:
		return VerdictRedOnBase
	case f.GreenOnBase != nil && f.GreenOnHead != nil:
		return VerdictInteraction
	}
	return VerdictUnknown
}

// Collision is a handback's account of what it collided with.
type Collision struct {
	Cause Cause
	// Guidance replaces the generic handback text; empty when the server has
	// nothing to add.
	Guidance string
	// Predecessors are the tickets whose landed changes are named.
	Predecessors []int64
	// Counts is whether this handback is a collision of the ticket: false for
	// a red check that is the change's own bug.
	Counts bool
}

// Detail is the string stored with the outcome row. It starts "collision " so
// History can count it.
func (c *Collision) Detail() string {
	ids := make([]string, len(c.Predecessors))
	for i, id := range c.Predecessors {
		ids[i] = fmt.Sprint(id)
	}
	return fmt.Sprintf("%s cause=%s predecessors=%s", DetailPrefix, c.Cause, strings.Join(ids, ","))
}

const (
	// DetailPrefix starts the detail of every handback that counts as a collision.
	DetailPrefix = "collision"
	// BoundPrefix starts the detail of the handback that reached the bound: the
	// count starts again after it.
	BoundPrefix = "collision_bound"
)

// Advisor writes the handback guidance for collisions with other landed work.
type Advisor struct {
	src   Source
	hist  History
	bound int
	log   *slog.Logger
}

// New builds an Advisor; a bound under one takes DefaultBound.
func New(src Source, hist History, bound int, h slog.Handler) *Advisor {
	if bound < 1 {
		bound = DefaultBound
	}
	return &Advisor{src: src, hist: hist, bound: bound, log: slog.New(h)}
}

// Conflict explains a merge-main conflict: who landed first, and where.
func (a *Advisor) Conflict(ctx context.Context, ticketID int64, attempt int, head string) *Collision {
	c := &Collision{Cause: CauseMergeConflict, Counts: true}
	landed, mine, ok := a.facts(ctx, ticketID, attempt, head)
	if !ok {
		return c
	}
	hit := overlapping(landed, mine)
	if len(hit) == 0 {
		// Conflicts need not show in the files (a rename, a file the host
		// would not list): name what landed anyway rather than nothing.
		hit = landed
	}
	c.Predecessors = ids(hit)
	c.Guidance = "The server tried to merge the base branch into your change and it conflicts. " +
		"It conflicts with work that landed after your ticket began:\n" + describe(hit, mine) +
		"\nMerge the base branch (origin/<base>) into your branch and resolve the conflicts in those files " +
		"with the intent of the changes above in mind: read them, do not just retry. The base now includes at least commit " + hit[len(hit)-1].MergeSHA +
		". Run the tests, push, and call ticket_finish again."
	return c
}

// Red explains checks that failed after the base was merged in cleanly.
func (a *Advisor) Red(ctx context.Context, ticketID int64, attempt int, head string, failing []string, facts Facts) *Collision {
	v := facts.verdict()
	c := &Collision{Cause: CauseInteraction, Counts: v == VerdictInteraction}
	if v != VerdictInteraction {
		c.Guidance = redText(v, failing)
		return c
	}
	landed, mine, ok := a.facts(ctx, ticketID, attempt, head)
	if ok {
		c.Predecessors = ids(landed)
	}
	c.Guidance = fmt.Sprintf("The server's checks failed on %s after it merged the base branch into your change: %s. "+
		"The same checks are green on the base's tip and on your change before the merge, so the failure comes from your change together with what landed since:\n%s\n"+
		"Merge the base branch into your branch, find how the two interact, fix it in that context, push, and call ticket_finish again.",
		head, strings.Join(failing, ", "), describe(landed, mine))
	return c
}

func redText(v Verdict, failing []string) string {
	names := strings.Join(failing, ", ")
	switch v {
	case VerdictOwnBug:
		return fmt.Sprintf("The server's checks failed: %s. They were red on your change before the base was merged in, so this is your change's own failure. Fix them, push, and call ticket_finish again.", names)
	case VerdictRedOnBase:
		return fmt.Sprintf("The server's checks failed: %s. They are red on the base's tip too, so look at what landed there as well as at your change. Fix what is yours, push, and call ticket_finish again.", names)
	}
	return fmt.Sprintf("The server's checks failed: %s. Fix them, push, and call ticket_finish again.", names)
}

// AtBound is whether this collision is the one that stops the re-queueing: the
// ticket was handed back bound-1 times before for collisions.
func (a *Advisor) AtBound(ctx context.Context, ticketID int64) (prior int, atBound bool) {
	n, err := a.hist.Collisions(ctx, ticketID)
	if err != nil {
		// Not knowing is a handback, which a human sees again next time.
		a.log.WarnContext(ctx, "landing: could not count collisions", "ticket", ticketID, "err", err)
		return 0, false
	}
	return n, n+1 >= a.bound
}

// Discussion is the question and handoff for a ticket that reached the bound.
func (a *Advisor) Discussion(prior int, c *Collision, thisGuidance string) (question, handoff string) {
	// The marker is a line of its own: the steward reads it as a whole line.
	question = fmt.Sprintf("[human-only]\nThis ticket has collided with other landed work %d times (the server's bound is %d): "+
		"two tickets may really be one piece of work. Should it be merged with, or sequenced after, %s?", prior+1, a.bound, ticketList(c.Predecessors))
	handoff = "The server's landing queue stopped re-queueing this ticket after repeated collisions with changes that landed first. " +
		"The latest collision:\n" + thisGuidance
	return question, handoff
}

func ticketList(ids []int64) string {
	if len(ids) == 0 {
		return "the tickets that landed first"
	}
	s := make([]string, len(ids))
	for i, id := range ids {
		s[i] = fmt.Sprintf("#%d", id)
	}
	return strings.Join(s, ", ")
}

func (a *Advisor) facts(ctx context.Context, ticketID int64, attempt int, head string) (landed []Landed, mine []string, ok bool) {
	landed, err := a.src.LandedSince(ctx, ticketID, attempt)
	if err != nil {
		a.log.WarnContext(ctx, "landing: could not list what landed", "ticket", ticketID, "err", err)
		return nil, nil, false
	}
	mine, err = a.src.HeadFiles(ctx, ticketID, attempt, head)
	if err != nil {
		a.log.WarnContext(ctx, "landing: could not read the change's files", "ticket", ticketID, "err", err)
	}
	return landed, mine, len(landed) > 0
}

func overlapping(landed []Landed, mine []string) []Landed {
	var out []Landed
	for _, l := range landed {
		if len(shared(l.Files, mine)) > 0 {
			out = append(out, l)
		}
	}
	return out
}

func shared(a, b []string) []string {
	var out []string
	for _, f := range a {
		if slices.Contains(b, f) {
			out = append(out, f)
		}
	}
	return out
}

func ids(ls []Landed) []int64 {
	out := make([]int64, len(ls))
	for i, l := range ls {
		out[i] = l.TicketID
	}
	return out
}

// maxFiles bounds the files named per change, so a large landing does not fill the comment.
const maxFiles = 10

func describe(ls []Landed, mine []string) string {
	var b strings.Builder
	for _, l := range ls {
		files := shared(l.Files, mine)
		kind := "conflicting files"
		if len(files) == 0 {
			files, kind = l.Files, "files"
		}
		more := ""
		if len(files) > maxFiles {
			more = fmt.Sprintf(" and %d more", len(files)-maxFiles)
			files = files[:maxFiles]
		}
		// Paths are chosen by whoever landed the change and this text is read by
		// an agent as instructions, so they are quoted as data.
		quoted := make([]string, len(files))
		for i, f := range files {
			quoted[i] = strconv.Quote(f)
		}
		fmt.Fprintf(&b, "- ticket #%d, %s (merge commit %s), %s: %s%s\n", l.TicketID, l.ChangeURL, l.MergeSHA, kind, strings.Join(quoted, ", "), more)
	}
	return strings.TrimRight(b.String(), "\n")
}
