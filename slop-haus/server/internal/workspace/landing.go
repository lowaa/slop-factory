package workspace

// landingRules is the working agreement's two bullets on how a ticket's
// change reaches the base branch. They differ by who drives the code host: with a
// connected credential slop-haus opens, checks and merges the change itself
// (and enforces the lock, the checks and the approval); without one the
// agent does it with whatever tool the repo's CLAUDE.md names.
//
// The briefing is written when a workspace is set up or updated, so the
// credential can be disconnected after it was written. The change-tools text
// says what to do then rather than leaving the agent stuck.
func landingRules(changeTools bool) string {
	if changeTools {
		return baseCommitRule + landingRulesChangeTools + landingQueueRule
	}
	return baseCommitRule + landingRulesOwnTools
}

// baseCommitRule is where a ticket's branch comes from. The server pins the base
// branch's head when a ticket is first claimed, so that a second attempt added later
// starts from the same commit as the first, and makes the attempt's branch at
// it. A ticket it could not pin, such as one claimed while the code host was
// unreachable, has no such branch: the agent makes its own from the base branch as before.
// baseBranchDef defines the term the rest of the rules use. The briefing is
// written once for the workspace, so it can't name a branch: the ticket's own
// base comes in the tool results (see ticket.BranchGuidance).
const baseBranchDef = "<base> is the ticket's base branch, the one its change merges into:\n" +
	"  the repository's default branch unless `ticket_next` or `ticket_get` name another."

const baseCommitRule = "- Work on the branch slop-haus made for your attempt: `ticket_next` and\n" +
	"  `ticket_get` name it (\"Your branch ... is ready\"); `git fetch origin`,\n" +
	"  then `git switch <branch>`. If it already has commits, continue from them.\n" +
	"  Don't make a branch of your own: when they say it is not ready, call\n" +
	"  `ticket_get` to have it made again. Only when the ticket is not pinned\n" +
	"  (`ticket_next` says so) make the branch they name yourself, from\n" +
	"  `origin/<base>`. " + baseBranchDef + " Merging the base branch into your\n" +
	"  branch before you merge (below) is unchanged.\n"

// These are the landing rules' wording that assumes the agent claims
// for itself; the headless role swaps them (see headlessRole).
const (
	lockQueueStep = "  4. take `integration_lock` -- if it is held you are queued, and asking\n" +
		"     again keeps your place: wait as the waiting rule says, then ask again;\n"
	reviewComesBack = "`ticket_next` and\n" +
		"  `ticket_get` say what to do with it when it comes back. The server may\n" +
		"  instead merge an approved change itself and close the ticket as done:\n" +
		"  a ticket you find done has nothing left to do.\n"
)

// A ticket that does not require approval is merged by the agent; the server
// refuses a request for review on it without a reason.
const voluntaryReview = "- On a ticket that does not require approval you merge the change yourself.\n" +
	"  `ticket_finish` with `request_review` there is rare and needs a `reason`: it is for a finished\n" +
	"  change whose code you want a human to read before it merges (risky,\n" +
	"  security-sensitive, a migration you are unsure of). A question about\n" +
	"  direction, spec or approach is not a review: ask it early, before\n" +
	"  building it, with `ticket_discuss`.\n"

// mergeMainOnce ends step 3. Main can move faster than a CI run, so without
// an end condition a worker keeps merging the base branch and never reaches the lock.
const mergeMainOnce = "Do this once: merge the base branch and get\n" +
	"     the checks green a single time, then take the lock even if the base branch has moved\n" +
	"     since. Step 5 deals with that move under the lock; never merge the base branch again\n" +
	"     before the lock just because it moved, and never to avoid waiting in the\n" +
	"     lock's queue (a queued wait is cheaper than a CI run; if the lock is held,\n" +
	"     queue for it);"

const landingRulesOwnTools = "- A ticket's work reaches the base branch through a reviewable change (a pull\n" +
	"  request) that you open and merge yourself. The repo's CLAUDE.md says\n" +
	"  which tool to use and how; the description links back to the ticket with\n" +
	"  `slop-haus #<id>` and the `url` that `ticket_get` returns. Open it early,\n" +
	"  as a draft where the tool has drafts, so the board shows it while you\n" +
	"  work and CI runs on every push:\n" +
	"  1. at your first push, push your branch and open the change as a draft,\n" +
	"     its description written then; push as the work goes;\n" +
	"  2. once the work is finished, take the change out of draft (if the tool\n" +
	"     has no drafts, it was ready when you opened it);\n" +
	"  3. merge the base branch into your branch, resolve any conflicts, re-run the\n" +
	"     tests and push -- all before the lock. " + mergeMainOnce + "\n" +
	lockQueueStep +
	"  5. under the lock only re-check: `git fetch origin`. If your head\n" +
	"     already contains the base branch's tip, merge the change. If the base branch has moved and\n" +
	"     merging it is clean and the only overlap with your change is inside\n" +
	"     the workspace's shared paths (or there is none), merge it,\n" +
	"     re-run the tests, push, and merge the change. If it conflicts or touches other paths of yours,\n" +
	"     release the lock with `integration_unlock`, resolve and test outside\n" +
	"     it, and queue again from step 4. Release the lock when done;\n" +
	"  6. finish with `ticket_finish`, passing the change's URL as `pr_url`.\n" +
	"  The lock stops every other agent from merging, so hold it only for that\n" +
	"  quick re-check and the merge, never for a fix or for more than one run of\n" +
	"  CI. If a check on your locked push fails, release the lock with\n" +
	"  `integration_unlock` before investigating; a flaky check is re-run\n" +
	"  outside the lock, and a second failure goes to `ticket_discuss`.\n" +
	"  If the repo has no way to open one, merge as its CLAUDE.md says, under\n" +
	"  the same lock, and leave `pr_url` out.\n" +
	"- A ticket that requires human approval is not merged by you: once the\n" +
	"  work is finished, take the change out of draft (step 2) and call\n" +
	"  `ticket_finish` with its URL as `pr_url` and a handoff. " + reviewComesBack + voluntaryReview

// landingQueueRule is the other way a change lands, for a tenant that lets the
// server do it. The briefing is written once for the workspace and can't know
// the tenant's choice, so ticket_finish's answer says which applies.
const landingQueueRule = "- Where this tenant lands by the server's merge queue, you do not take the\n" +
	"  lock: once the change is out of draft (step 2) and the tests pass locally,\n" +
	"  call `ticket_finish`, which says \"in the landing queue\", and your run is over.\n" +
	"  slop-haus then merges the base branch into the change, waits for CI and\n" +
	"  merges it. If it can't land (a conflict, red checks, a refusal) the ticket\n" +
	"  comes back to you with the reason: fix it, push and call `ticket_finish`\n" +
	"  again. When `ticket_finish` instead says you still hold the ticket, follow\n" +
	"  steps 3 to 6 above.\n"

const landingRulesChangeTools = "- A ticket's work reaches the base branch through a reviewable change (a pull\n" +
	"  request). This tenant has a code host credential connected, so\n" +
	"  slop-haus opens and merges the\n" +
	"  change for you, with that credential: use `change_open`, `change_checks`\n" +
	"  and `change_merge`, not the host's own CLI or a token of your own. The\n" +
	"  repo's CLAUDE.md says how to name the branch and describe the change;\n" +
	"  the description links back to the ticket with `slop-haus #<id>` and the\n" +
	"  `url` that `ticket_get` returns. The change is opened early, as a\n" +
	"  draft, so the board shows it while you work and CI runs on every push:\n" +
	"  1. at your first push, push your branch and call `change_open` with it\n" +
	"     and `draft` true, its description written then; push as the work\n" +
	"     goes. `change_open` does not change the description of a change\n" +
	"     that is already open, so anything to add to it later goes in a\n" +
	"     ticket comment;\n" +
	"  2. once the work is finished, call `change_open` again with\n" +
	"     the same branch and `draft` false, which takes it out of draft;\n" +
	"  3. merge the base branch into your branch, resolve any conflicts, re-run the\n" +
	"     tests and push; then wait for `change_checks` (with the commit you\n" +
	"     pushed as `head_sha`) to say they passed. Checks that are for another\n" +
	"     head, or none yet, are not a pass. All of this is before the lock.\n" +
	"     " + mergeMainOnce + "\n" +
	lockQueueStep +
	"  5. under the lock only re-check: `git fetch origin`. If your head\n" +
	"     already contains the base branch's tip, call `change_checks` on it and then\n" +
	"     `change_merge`. If the base branch has moved and merging it is clean and the only\n" +
	"     overlap with your change is inside the workspace's shared paths (of any\n" +
	"     kind, or there is none), merge it, push, wait for `change_checks`\n" +
	"     on the new head (one check run) and call `change_merge`. If it\n" +
	"     conflicts or touches other paths of yours, release the lock with\n" +
	"     `integration_unlock`, resolve and test outside it, and queue again\n" +
	"     from step 4;\n" +
	"  6. call `change_merge` (with `head_sha`). A merged change closes the\n" +
	"     ticket as done with the change's URL and releases the lock itself:\n" +
	"     you are finished, with nothing more to call. (If it says it could not\n" +
	"     close the ticket, call `integration_unlock` and `ticket_finish` with\n" +
	"     the change's URL as `pr_url`.)\n" +
	"  `change_merge` is refused unless you hold the lock, the change is out of\n" +
	"  draft, the checks passed on the change's current head, that head contains\n" +
	"  the base branch's latest commit and, where the ticket requires approval, a human\n" +
	"  approved it; the refusal says which. Everything else above is advice the\n" +
	"  server does not check: merging the base branch and testing before the lock, and\n" +
	"  releasing the lock to fix. Never wait on CI under the lock for longer than\n" +
	"  one check run; if checks fail on the re-check, release the lock before\n" +
	"  investigating with `integration_unlock`; a flaky check is re-run\n" +
	"  outside the lock, and a second failure goes to `ticket_discuss`.\n" +
	"  Only a change `change_open` opened can be merged this\n" +
	"  way: a pull request you opened yourself cannot. If a change tool answers\n" +
	"  that the tenant has no code host credential, it was disconnected after\n" +
	"  this file was written: land the change as the repo's CLAUDE.md says\n" +
	"  instead, under the same lock, and say so in a ticket comment.\n" +
	"- A ticket that requires human approval is not merged by you: once the\n" +
	"  work is finished, take the change out of draft (step 2) and call\n" +
	"  `ticket_finish` with its URL as `pr_url` and a handoff. " + reviewComesBack + voluntaryReview
