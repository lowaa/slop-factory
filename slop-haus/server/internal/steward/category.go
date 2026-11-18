// Package steward decides which of the owner's routine approvals a steward
// agent may handle for them, and how.
//
// The steward only proposes a category and a reason. Whether the category's
// conditions hold is decided here, from facts the server reads itself (CI,
// diffs, the ticket's state), never from the proposer's word. Anything that
// does not match a category, or whose checks fail, is the owner's to decide.
package steward

import "slices"

// Tier is how much of the owner's say a category gives up.
type Tier string

const (
	// TierAuto is applied at once.
	TierAuto Tier = "auto"
	// TierVeto is applied after a window unless the owner vetoes it.
	TierVeto Tier = "veto"
	// TierHuman is always the owner's decision.
	TierHuman Tier = "human"
)

// autonomy orders tiers by how little the owner is involved, so "raising" a
// category is a comparison rather than a switch.
func (t Tier) autonomy() int {
	switch t {
	case TierAuto:
		return 2
	case TierVeto:
		return 1
	default:
		return 0
	}
}

func (t Tier) valid() bool { return t == TierAuto || t == TierVeto || t == TierHuman }

// Category is one kind of decision the steward may take.
type Category struct {
	ID string
	// Class picks the category's default tier from the tenant's level; the
	// owner may override it per tenant.
	Class Class
	// NeverAuto categories are the owner's whatever the settings say. None is
	// handled today (anything outside the list is Human); the flag is how a
	// category is listed to be shown and refused.
	NeverAuto bool
	// Kinds are the proposal kinds the category may cover.
	Kinds []string
	// EditFields are the fields of an edit proposal the category may change;
	// an edit that touches any other is the owner's.
	EditFields []string
	// Checks are the server checks that must all pass.
	Checks []string
	// Evidence says what the category stands for and what the checks prove.
	Evidence string
}

const (
	CheckCIGreenOnHead      = "ci_green_on_head"
	CheckDiffWithinScope    = "diff_within_scope"
	CheckNoNewPaths         = "no_new_paths"
	CheckTicketRevUnchanged = "ticket_rev_unchanged"
	CheckHeadUnchanged      = "head_unchanged"
	CheckNoHumanOnlyMarker  = "no_human_only_marker"
	CheckTicketPreparing    = "ticket_preparing"
	CheckApprovedPlan       = "approved_plan"
	CheckEditFieldsAllowed  = "edit_fields_allowed"
	CheckAnswerFromTemplate = "answer_from_template"

	// The checks of the judgment categories. The names are shared so each
	// category's part only adds its own files.
	CheckNoProtectedPath          = "no_protected_path"
	CheckDiffSizeWithinLimit      = "diff_size_within_limit"
	CheckSlopReviewComplete       = "slop_review_complete"
	CheckNoReviewRequestReason    = "no_review_request_reason"
	CheckBlockersRecorded         = "blockers_recorded"
	CheckTicketHasBrief           = "ticket_has_brief"
	CheckOptionRecommendedByAgent = "option_recommended_by_worker"
	CheckTopicNotSensitive        = "topic_not_sensitive"
)

const (
	KindDiscussionAnswer = "discussion_answer"
	KindReady            = "ready"
	KindEdit             = "edit"
	// KindReviewDecision is a review of a change; the steward may only approve.
	KindReviewDecision = "review_decision"
)

// categories is the allowlist. Adding to it is a rule change: it ships with
// the code, versioned with the checks it names.
var categories = []Category{
	{
		ID: "merge_continue", Class: ClassMechanical, Kinds: []string{KindDiscussionAnswer},
		Checks:   []string{CheckCIGreenOnHead, CheckHeadUnchanged, CheckDiffWithinScope, CheckTicketRevUnchanged, CheckNoHumanOnlyMarker, CheckAnswerFromTemplate},
		Evidence: "Work is done and CI is green on the change's current head, within the ticket's scope: tell the agent to carry on and merge.",
	},
	{
		ID: "infra_wait", Class: ClassMechanical, Kinds: []string{KindDiscussionAnswer},
		Checks:   []string{CheckHeadUnchanged, CheckTicketRevUnchanged, CheckNoHumanOnlyMarker, CheckAnswerFromTemplate},
		Evidence: "An outside system (CI, the code host) is delayed and nothing on the ticket has moved: tell the agent to wait.",
	},
	{
		ID: "gate_confirmed", Class: ClassMechanical, Kinds: []string{KindDiscussionAnswer},
		Checks:   []string{CheckCIGreenOnHead, CheckHeadUnchanged, CheckTicketRevUnchanged, CheckNoHumanOnlyMarker, CheckAnswerFromTemplate},
		Evidence: "A gate the agent asked about is satisfied on the head it names, as the server reads it.",
	},
	{
		// Blockers and child tickets are not fields of an edit proposal, so no
		// edit qualifies as this today.
		ID: "bookkeeping", Class: ClassMechanical, Kinds: []string{KindEdit},
		Checks:   []string{CheckTicketRevUnchanged, CheckNoNewPaths, CheckEditFieldsAllowed, CheckApprovedPlan, CheckNoProtectedPath},
		Evidence: "Blockers and child tickets from an already approved plan; claims no path the ticket does not already have.",
	},
	{
		ID: "narrow_scope", Class: ClassHousekeeping, Kinds: []string{KindEdit}, EditFields: []string{"paths"},
		Checks:   []string{CheckTicketRevUnchanged, CheckNoNewPaths, CheckEditFieldsAllowed, CheckNoProtectedPath},
		Evidence: "Shrink a ticket's paths to a subset of what it has, never add one.",
	},
	{
		ID: "priority", Class: ClassHousekeeping, Kinds: []string{KindEdit}, EditFields: []string{"priority"},
		Checks:   []string{CheckTicketRevUnchanged, CheckEditFieldsAllowed},
		Evidence: "Change a priority within guidance the owner already gave.",
	},
	{
		ID: "review_approve", Class: ClassJudgment, Kinds: []string{KindReviewDecision},
		Checks: []string{CheckCIGreenOnHead, CheckHeadUnchanged, CheckTicketRevUnchanged, CheckDiffWithinScope, CheckDiffSizeWithinLimit,
			CheckSlopReviewComplete, CheckNoReviewRequestReason, CheckNoProtectedPath, CheckNoHumanOnlyMarker},
		Evidence: "Approve a small change inside the ticket's scope, with CI green and the supervisor's slop-review of its head approving with no blocker or major finding, that avoids protected paths and that the agent did not ask a human to read.",
	},
	{
		// The checks it adds are in ticketready, which imports this package;
		// ticketready's tests pin this entry.
		ID: "ticket_ready", Class: ClassJudgment, Kinds: []string{KindReady},
		Checks: []string{CheckTicketHasBrief, CheckBlockersRecorded, CheckTicketPreparing, CheckTicketRevUnchanged, CheckNoNewPaths,
			CheckNoProtectedPath, CheckNoHumanOnlyMarker},
		Evidence: "Open a preparing ticket whose brief the server has read: it names its paths, a \"Do\" and its tests, records its blockers, touches no protected path, and is unchanged since the assessment.",
	},
	{
		// The checks it adds are in discussionpick, which imports this
		// package; discussionpick's tests pin this entry.
		ID: "discussion_pick", Class: ClassJudgment, Kinds: []string{KindDiscussionAnswer},
		Checks: []string{CheckOptionRecommendedByAgent, CheckTopicNotSensitive, CheckNoHumanOnlyMarker,
			CheckHeadUnchanged, CheckTicketRevUnchanged, CheckNoProtectedPath},
		Evidence: "Answer a worker's question by accepting, unchanged, the option its handoff recommends, when the topic is not on the owner's sensitive word list, touches no protected path and the discussion is not reserved for a human.",
	},
}

// retired are categories removed from the allowlist (open_ticket folded into ticket_ready). Assessments stored under
// them stay readable, and a tier override naming one is dropped rather than
// refused, so settings saved before the removal still load.
var retired = []string{"restart_resume", "open_ticket"}

// Retired reports whether id names a category that was removed.
func Retired(id string) bool { return slices.Contains(retired, id) }

// Categories is the allowlist, as a copy.
func Categories() []Category {
	out := make([]Category, len(categories))
	for i, c := range categories {
		c.Kinds = slices.Clone(c.Kinds)
		c.Checks = slices.Clone(c.Checks)
		c.EditFields = slices.Clone(c.EditFields)
		out[i] = c
	}
	return out
}

// Lookup finds a category by id.
func Lookup(id string) (Category, bool) {
	for _, c := range Categories() {
		if c.ID == id {
			return c, true
		}
	}
	return Category{}, false
}

// EffectiveTier is the tier a category runs at under the tenant's level and
// overrides, in order: NeverAuto is Human; a valid override wins over the
// level in either direction; otherwise the level's tier for the class. A level
// below Routine applies nothing, so it yields Human whatever an override says
// (the override stays stored, inert).
func EffectiveTier(c Category, level Level, overrides TierOverrides) Tier {
	if c.NeverAuto || !level.Applies() {
		return TierHuman
	}
	if t, ok := overrides[c.ID]; ok && t.valid() {
		return t
	}
	return level.DefaultTier(c.Class)
}
