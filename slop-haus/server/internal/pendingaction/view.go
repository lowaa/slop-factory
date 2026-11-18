package pendingaction

import (
	"context"
	"fmt"
	"slices"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// View is a proposal with what the person needs to decide on it.
type View struct {
	Action
	AgentName    string
	TicketTitle  string
	TicketStatus ticket.Status
	// ActionLine says what approving does, built here from the kind, the
	// payload's structured fields and the ticket's id and status: never from
	// the proposer's words, so prose cannot dress a destructive move up as
	// a harmless one. The proposer's text is Payload.Body, shown apart.
	ActionLine string
	// StaleReason is, for a pending proposal, why approving it now would be
	// refused because of the ticket; empty when it would not.
	StaleReason string
	// Edit is, for an edit proposal, what approving it changes.
	Edit *EditView
	// ManifestEdit is, for a manifest_edit proposal, what approving it changes.
	ManifestEdit *ManifestEditView
	// CloseMerged is, for a close_merged proposal, what the code host says now.
	CloseMerged *CloseMergedView
	// Assessment is the steward's verdict, nil when it made none.
	Assessment *AssessmentView
	// Draft is the steward's pre-drafted decision, nil when it made none.
	Draft *DraftView
}

// TextChange is a title or body moving from Old to New. Old is nil once the
// proposal is decided: the ticket no longer says what it did.
type TextChange struct {
	Old *string
	New string
}

type PriorityChange struct {
	Old *model.Priority
	New model.Priority
}

// EditView is an edit proposal against the ticket as it is now: only what
// would differ, so the person approves what they can see. Overlaps are the
// live tickets the new paths would overlap, which the board shows and does
// not refuse -- see ticket.UpdateInTx for what it does.
type EditView struct {
	Title        *TextChange
	Body         *TextChange
	Priority     *PriorityChange
	Paths        []string
	PathsAdded   []string
	PathsRemoved []string
	Overlaps     []ticket.ScopeOverlap
	Open         bool
}

// Views lists proposals as filter selects them, with their descriptions.
func (s *Service) Views(ctx context.Context, filter *ListFilter) ([]View, error) {
	actions, err := s.List(ctx, filter)
	if err != nil {
		return nil, err
	}
	return s.describe(ctx, actions)
}

// ViewOf is one proposal as a View, for a signed-in person.
func (s *Service) ViewOf(ctx context.Context, who *authn.User, id uuid.UUID) (*View, error) {
	if err := authn.RequirePerson(who); err != nil {
		return nil, err
	}
	a, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	views, err := s.describe(ctx, []Action{*a})
	if err != nil {
		return nil, err
	}
	return &views[0], nil
}

func (s *Service) describe(ctx context.Context, actions []Action) ([]View, error) {
	ids := make([]uuid.UUID, len(actions))
	for i := range actions {
		ids[i] = actions[i].ID
	}
	described, err := s.store.Describe(ctx, ids)
	if err != nil {
		return nil, err
	}
	var assessed map[uuid.UUID]*AssessmentView
	if s.assessments != nil && len(ids) > 0 {
		if assessed, err = s.assessments.Assessments(ctx, ids); err != nil {
			s.logger.WarnContext(ctx, "pending action: read assessments", "error", err)
		}
	}
	var drafted map[uuid.UUID]*DraftView
	if s.drafts != nil && len(ids) > 0 {
		if drafted, err = s.drafts.Drafts(ctx, ids); err != nil {
			s.logger.WarnContext(ctx, "pending action: read drafts", "error", err)
		}
	}
	views := make([]View, len(actions))
	for i := range actions {
		v := View{Action: actions[i], Assessment: assessed[actions[i].ID], Draft: drafted[actions[i].ID]}
		if d := described[actions[i].ID]; d != nil {
			v.AgentName, v.TicketTitle, v.TicketStatus = d.AgentName, d.TicketTitle, d.TicketStatus
			if v.State == StatePending {
				switch {
				case d.Held:
					v.StaleReason = "an agent now holds the ticket"
				default:
					v.StaleReason = actions[i].Basis.differsFrom(&d.Live)
				}
			}
		}
		v.ActionLine = ActionLine(&actions[i], v.TicketStatus)
		if actions[i].Kind == KindEdit {
			var drift string
			if v.Edit, drift, err = s.editView(ctx, &actions[i]); err != nil {
				return nil, err
			}
			if v.StaleReason == "" && drift != "" {
				v.StaleReason = drift
			}
		}
		if actions[i].Kind == KindManifestEdit {
			var drift string
			if v.ManifestEdit, drift, err = s.manifestView(ctx, &actions[i]); err != nil {
				return nil, err
			}
			if v.State == StatePending {
				v.StaleReason = drift
			}
		}
		views[i] = v
	}
	s.fillCloseMerged(ctx, views)
	return views, nil
}

// ActionLine is what approving a does, given the ticket's status now.
func ActionLine(a *Action, current ticket.Status) string {
	if spec := a.Kind.Spec(); spec.Subject == SubjectTenant {
		if spec.ActionLine != nil {
			return spec.ActionLine(a)
		}
		return "Unknown action on the workspace."
	}
	id := a.ticketID()
	switch a.Kind {
	case KindDiscussionAnswer:
		return fmt.Sprintf("Post the text below as your comment on #%d. That answers its discussion and puts it back in the queue.", id)
	case KindReady:
		return fmt.Sprintf("Move #%d out of preparing and into the queue, so an agent can pick it up.", id)
	case KindStatus:
		if a.Payload.Status == ticket.StatusCancelled {
			return fmt.Sprintf("Cancel #%d (now %s), posting the reason below as your comment. Cancelling closes it.", id, current)
		}
		return fmt.Sprintf("Park #%d (now %s), posting the reason below as your comment. Parked tickets are not worked until you unpark them.", id, current)
	case KindEdit:
		if a.Payload.Edit.Open {
			return fmt.Sprintf("Apply the edit below to #%d (now %s), as if you had made it on the board, and move it out of preparing and into the queue, so an agent can pick it up. Both happen together or not at all.", id, current)
		}
		return fmt.Sprintf("Apply the edit below to #%d (now %s), as if you had made it on the board. Its status does not change.", id, current)
	case KindCloseMerged:
		return fmt.Sprintf("Close #%d (now %s) as done, because its change was merged outside slop-haus. The code host is asked again when you approve, and the close is refused if the change is not merged into the ticket's base branch. Any agent holding it loses it.", id, current)
	case KindReviewDecision:
		if a.Payload.Decision == DecisionApprove {
			return fmt.Sprintf("Approve the change under review on #%d. The agent holding its PR may then merge it.", id)
		}
		return fmt.Sprintf("Send #%d back from review, posting the text below as what to address.", id)
	}
	return fmt.Sprintf("Unknown action on #%d.", id)
}

// editView compares an edit proposal to the ticket as it is now. A decided
// proposal, or a deployment that cannot read the ticket, shows only what it
// proposed.
func (s *Service) editView(ctx context.Context, a *Action) (*EditView, string, error) {
	e := a.Payload.Edit
	view := &EditView{Paths: e.Paths, Open: e.Open}
	var live *ticket.Ticket
	var drift string
	if a.State == StatePending && s.tickets != nil {
		var err error
		if live, err = s.tickets.Get(ctx, a.ticketID()); err != nil {
			return nil, "", fmt.Errorf("pending action: read ticket: %w", err)
		}
	}
	if live != nil {
		drift = e.drift(live)
	}
	if e.Title != nil && (live == nil || *e.Title != live.Title) {
		view.Title = &TextChange{New: *e.Title}
		if live != nil {
			view.Title.Old = &live.Title
		}
	}
	if e.Body != nil && (live == nil || *e.Body != live.Body) {
		view.Body = &TextChange{New: *e.Body}
		if live != nil {
			view.Body.Old = &live.Body
		}
	}
	if e.Priority != nil && (live == nil || *e.Priority != live.Priority) {
		view.Priority = &PriorityChange{New: *e.Priority}
		if live != nil {
			view.Priority.Old = &live.Priority
		}
	}
	if live == nil || e.Paths == nil {
		return view, drift, nil
	}
	var filed []string
	proposed := make(map[string]bool, len(e.Paths))
	for _, p := range e.Paths {
		proposed[p] = true
	}
	// The extended paths an agent was granted stay through an edit.
	scope := e.updateParams().Paths
	for _, p := range live.Paths {
		if p.Source != model.PathSourceFiled {
			scope = append(scope, p)
			continue
		}
		filed = append(filed, p.Path)
		if !proposed[p.Path] {
			view.PathsRemoved = append(view.PathsRemoved, p.Path)
		}
	}
	for _, p := range e.Paths {
		if !slices.Contains(filed, p) {
			view.PathsAdded = append(view.PathsAdded, p)
		}
	}
	overlaps, err := s.tickets.ScopeOverlapsWith(ctx, a.ticketID(), scope)
	if err != nil {
		return nil, "", fmt.Errorf("pending action: scope overlaps: %w", err)
	}
	view.Overlaps = overlaps
	return view, drift, nil
}
