package mcpserver

import "github.com/modelcontextprotocol/go-sdk/mcp"

// Register mounts every tool slop-haus exposes.
//
// Deliberately absent: a bare ticket_claim(id) separate from ticket_next.
// Cherry-picking defeats dependency ordering and priority, so ticket_next
// takes an optional ticket_id and applies the same conflict and dependency
// checks either way -- one code path, one set of invariants.
//
// Also absent: a general lock_acquire(name). A lock whose lifetime derives
// from a ticket lease only makes sense for a holder that has one, and a
// name parameter would let an agent create a lock with no lease behind it
// and no way to ever expire.
//
// Each tool's leaseNeed -- the argument before its handler -- is the one
// place that says whether it needs the ticket the agent holds, and so
// whether a revocation notice fails it or rides on its result.
func Register(server *mcp.Server, h *Handler) {
	addTool(server, h, &mcp.Tool{
		Name:        "checkin",
		Description: "Confirm who you are and what you are holding. Call this first.",
	}, leaseFree, h.checkin)

	addTool(server, h, &mcp.Tool{
		Name: "ticket_next",
		Description: "Claim the next ticket you can take, or a specific one. " +
			"Refuses if you already hold a ticket. Never grants a ticket in preparing -- a human is " +
			"still shaping it. May report the queue as blocked " +
			"by another agent's path scopes, or ticket processing as switched off. " +
			"A console claims only a ticket it is told to take, by id; asked for the queue it is refused.",
	}, leaseFree, h.ticketNext)

	addTool(server, h, &mcp.Tool{
		Name:        "ticket_get",
		Description: "Read a ticket: its scopes, its comments, its attachments (ticket_attachment_get reads one) and the latest handoff left on it.",
	}, leaseFree, h.ticketGet)

	addTool(server, h, &mcp.Tool{
		Name: "ticket_search",
		Description: "Find or list tickets: by the words in them (every word must appear somewhere on the ticket), " +
			"by status (statuses, e.g. [\"preparing\"]; text is then optional), or both -- e.g. to see whether a ticket " +
			"already exists before filing one, or to review everything in preparing. Listing is not claiming: " +
			"ticket_next is how work is taken. Searches what the board shows, and parked tickets too " +
			"(kept, not planned: never work to take); done ones only when asked. Newest first; ticket_get for detail.",
	}, leaseFree, h.ticketSearch)

	addTool(server, h, &mcp.Tool{
		Name:        "ticket_comment",
		Description: "Add a comment. Material decisions belong here -- the rest of the fleet cannot see your terminal. " + attemptArgDoc,
	}, leaseNamed, h.ticketComment)

	addTool(server, h, &mcp.Tool{
		Name: "ticket_edit",
		Description: "Change the title and/or description of the ticket you hold, when what it says no longer " +
			"matches the work. Pass the revision ticket_get showed; a body replaces the whole description. " +
			"The old text is kept in the ticket's history. " +
			"Only your own held ticket; to flag a problem with another, comment on it.",
	}, leaseHeld, h.ticketEdit)

	addTool(server, h, &mcp.Tool{
		Name: "ticket_scope_extend",
		Description: "Add paths to the ticket you hold. May be denied if another agent " +
			"holds a conflicting scope; narrow your approach or release the ticket.",
	}, leaseHeld, h.scopeExtend)

	addTool(server, h, &mcp.Tool{
		Name: "ticket_scope_narrow",
		Description: "Give up paths of the ticket you hold, so other agents can claim work on them. " +
			"Remove only paths the ticket has; keep[] may name narrower paths inside removed ones " +
			"(drop a directory, keep the sub-directory you still need). It never adds scope -- use " +
			"ticket_scope_extend for that -- and refuses to leave the ticket with no paths. " +
			"Never drop a path your branch has already changed. The server also narrows directories to the files your change touches when you ticket_finish, " +
			"ticket_discuss or ticket_release; call this earlier to free paths sooner, since where the tenant locks path scopes, a ticket in review holds all its paths until a human acts.",
	}, leaseHeld, h.scopeNarrow)

	addTool(server, h, &mcp.Tool{
		Name: "ticket_release",
		Description: "Hand your ticket back to the queue. Posts the reason as your comment (nothing when it is blank). " +
			"Waiting on another ticket: pass blocked_on, and it reopens when that ticket is done. Waiting on something " +
			"the queue cannot see (a deploy not yet rolled out, an outside system's state) or a transient failure: pass " +
			"retry_after, a duration like 30m, and nobody can claim the ticket until then; the reason must say what you " +
			"checked, how (the command or evidence) and what would show it is met, so the next check is one step. A " +
			"release naming neither blocked_on nor retry_after still keeps the ticket out of the queue for a short minimum, " +
			"so an immediate re-claim is not a loop; after a few such releases in a row the server moves the ticket to " +
			"discussion itself. Both together: it reopens when both clear. A release mid-work should also pass a " +
			"handoff naming the branch and the pushed commit, as ticket_finish asks: whoever claims it next may be on " +
			"another checkout. Omit the handoff and the previous one stays the latest. Waiting on a person's action or " +
			"decision is ticket_discuss, not a release: it parks the ticket until they answer.",
	}, leaseHeld, h.release)

	addTool(server, h, &mcp.Tool{
		Name: "ticket_discuss",
		Description: "Stop work on your ticket until the human answers a question. Posts the question " +
			"as your comment, records a handoff for whoever continues it, and releases the ticket. " +
			"Their reply puts it back in the queue, with you first in line. Commit and push your work to the ticket's " +
			"branch first, and name the branch and pushed commit in the handoff: whoever continues may be on another checkout.",
	}, leaseHeld, h.discuss)

	addTool(server, h, &mcp.Tool{
		Name: "ticket_prepare",
		Description: "Send the ticket you hold back to preparing, for a human to reshape before anyone works it " +
			"-- when it is not ready to build: the spec is wrong, too big, or not what is needed. Posts the reason " +
			"as your comment and releases the ticket. Only your own held ticket; to flag a problem with another, comment on it. " +
			"For a question you need answered to carry on, use ticket_discuss instead.",
	}, leaseHeld, h.prepare)

	registerFinish(server, h)

	addTool(server, h, &mcp.Tool{
		Name: "ticket_create",
		Description: "File a follow-up ticket. Does not claim it. It starts in preparing, where no agent can take it " +
			"until a human opens it. Pass status open only when you were specifically asked to file it as open.",
	}, leaseFree, h.ticketCreate)

	addTool(server, h, &mcp.Tool{
		Name: "ticket_blocker_add",
		Description: "Record that a ticket waits on another: it is not handed out until that one is done or cancelled. " +
			"A worker may do this only on the ticket it holds, or one it filed that nobody has started; to ask for another, comment on it. " +
			"A conductor, or a console with a live full-access grant, acts for its owner and may do it on any ticket in the tenant, " +
			"whatever its status and whoever holds it. An open ticket becomes blocked. " +
			"Recorded as your comment, naming the person you act for, and in the audit log.",
	}, leaseNamed, h.blockerAdd)

	addTool(server, h, &mcp.Tool{
		Name: "ticket_blocker_remove",
		Description: "A worker may remove only a blocker it added itself, on the ticket it holds or one it filed that nobody has started; " +
			"a blocker a human added is theirs to lift, so comment instead. " +
			"A conductor, or a console with a live full-access grant, acts for its owner and may remove any blocker on any ticket in the tenant; " +
			"removing the last one unblocks the ticket. Recorded as your comment, naming the person you act for, and in the audit log.",
	}, leaseNamed, h.blockerRemove)

	addTool(server, h, &mcp.Tool{
		Name: "integration_lock",
		Description: "Take the merge lock for the final re-check and merge. Merge the ticket's base branch into your branch, resolve conflicts, test and wait for green checks BEFORE taking it; under it only fetch, confirm the base branch has not moved (or that merging it is clean and touches none of your paths), and change_merge. Pins your lease until released, so release it before investigating a failed check. " +
			"If it is held, or reserved for an agent ahead of you, you join the queue (once) and the refusal says your " +
			"position, who is ahead and since when, and who holds it: end your turn at that wait point rather than retrying; you are resumed when the lock is yours. " +
			"Refused on an attempt that is not the ticket's promoted one, which cannot merge: finish with ticket_finish.",
	}, leaseHeld, h.integrationLock)

	addTool(server, h, &mcp.Tool{
		Name:        "integration_unlock",
		Description: "Release the merge lock.",
	}, leaseHeld, h.integrationUnlock)

	if h.manifests != nil {
		registerWorkspaceGet(server, h)
	}

	if h.batches != nil {
		registerLandingBatches(server, h)
	}

	if h.changes != nil {
		registerChangeTools(server, h)
	}

	if h.reviews != nil {
		registerReviewTools(server, h)
	}

	if h.proposals != nil && h.proposers != nil {
		registerConsoleTools(server, h)
	}

	if h.stewards != nil && h.assessor != nil {
		registerStewardTools(server, h)
	}

	if h.stewards != nil && h.drafter != nil {
		registerStewardDraftTool(server, h)
	}

	if h.stewards != nil && h.labeller != nil {
		registerStewardLabelTool(server, h)
	}

	// Always offered, as the contract's role lists name them: without an
	// object store they answer that attachments are not available, as the
	// routes answer 503.
	registerAttachmentTools(server, h)

	if h.usage != nil && h.proposers != nil {
		registerWorkingOn(server, h)
	}
}
