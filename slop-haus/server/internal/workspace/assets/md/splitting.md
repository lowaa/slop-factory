## Splitting large tickets

Large pull requests get worse reviews from both humans and slop-review, whose one pass has to hold the whole change. Keep each change small enough to review well, and land large work on the base branch in parts.

**When to split.** Once you've read the code and settled an approach, but before building, check two things:
- **Size:** hand-written lines, tests included, generated files excluded (golden files, generated API clients, lockfiles). Over about 2,000, plan a split.
- **Width:** whether the work is many independent pieces, e.g. the same change across many components or packages. If so, split it so the pieces can run in parallel, even when each is small.

**How to split.** Every part lands on the base branch by itself and leaves it working: it builds, passes tests, and changes nothing a user sees until it's meant to. Don't use a long-lived feature branch: it only moves the big merge to the end. Instead:
- Land a refactor first, so the behaviour change that follows is small.
- Put new code behind an injected interface: introduce it in front of the existing behaviour, build the new implementation behind it, then switch which one is wired in. The choice is then configuration, not an edit at every call site. Only add an interface where there will really be a second implementation or a switch; don't add layers the split doesn't need.
- Land new packages, handlers or components unwired: tested, but not yet called. The part that lands something unwired names the ticket that wires it, and that ticket must exist and be in your `blocked_on` before you release the parent. Unwired code is otherwise orphaned: nothing else asks for the wiring. CI lists exported functions no main reaches; the part adds a line naming the wiring ticket to `slop-haus/server/deadcode/allowlist.txt`, and the wiring ticket deletes it.
- Put user-visible changes behind a flag that's off by default.
- Change schemas and APIs expand–contract: add the new shape, move callers over, then remove the old shape.
- Keep mechanical changes (renames, moves, regeneration) in their own part, separate from behaviour changes.

**Splitting for parallel work.**
- **List the files each piece writes, then split along those lines.** Pieces that share a file (e.g. a stylesheet) are one part.
- **Put edits to shared files (config, shared helpers, test setup) in a first part,** and block the rest on it.
- **Never create a file that every part must edit** (an allowlist, registry or index): the parts will all conflict. Instead, add a final part, blocked on all the others, that checks the result.
- **Scope each part to its own files or leaf directory, never a shared parent.** A broad scope stops every overlapping ticket from being claimed. Read the `scope_overlaps` that `ticket_create` returns, and narrow anything that overlaps more than intended. Overlaps inside the workspace's shared paths (migrations, generated output) don't block a claim and are reported as shared, so they don't need narrowing.
- **Set `blocked_on` when you file each part,** so the order is enforced rather than left to whoever picks it up.

**Filing the parts.**
1. File each part with `ticket_create`. Give it a body that stands alone (what to build, what's out of scope, how it stays safe on the base branch) and the narrowest paths it needs. They start in preparing; a human opens them.
2. Account for everything on the ticket you hold: each requirement, and each decision in its comments, goes to a part, to your final step, or to a ticket that already exists (search, or file one, blocked on the parts). A deferral that names no ticket is a dropped requirement.
3. Comment the plan on the ticket you hold: the parts, their order, what the final step is, and where each requirement and decision went.
4. Release your ticket with `ticket_release`, `blocked_on` the parts. It comes back once they are done.
5. When it comes back, do what's left: usually switching the feature on and removing the old path and any flags. If nothing is left, finish with `ticket_finish` with `no_change` true, and say in the `reason` which tickets and PRs the work landed through.

**Finishing without a PR.** Close a ticket with no change of its own using `ticket_finish` with `no_change` true; the `reason` must say where the work landed, or why no change was needed.

**When it can't be split.** Some changes have to land whole, for example:
- a contract that must change on both sides at once
- a security change that is unsafe half-applied
- a migration whose code can't run against either schema alone

Try the techniques above first. If none of them gives parts that are each safe on the base branch, build it as one change, and:
- Record the reason with `ticket_comment` before building: why it can't be split, and what you tried.
- Add a "Size" line to the PR body: hand-written lines, and why it wasn't split.
- Have it reviewed in full: in a supervisor's slot with `review_request`, otherwise as your repo's instructions describe, over the whole change. Treat any review that comes back incomplete or with warnings as not having reviewed the change.
