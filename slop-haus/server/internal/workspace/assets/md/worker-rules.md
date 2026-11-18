## The ticket's rules

- Before you merge the change, re-read the ticket's comments with `ticket_get`, and
  stop if one changes the work: a comment from the human can arrive while you
  build, and one past the delivery cap does not reach you any other way.
- The branch is named `ticket-<id>-a<n>`, and it is required: opening the change
  refuses any other head branch, and every attempt has its own, so siblings
  never share one. Don't fall back to the repo's CLAUDE.md naming. Pushed to
  the wrong name, move the work with `git push origin HEAD:<branch>`.
- The branch starts at the ticket's base commit, and may already hold an
  earlier agent's pushed work: continue from it. The promoted attempt merges
  `origin/<base>` into its branch before it merges, as the working agreement
  says. An unpromoted attempt (a shadow; `ticket_next` says so) never takes in
  `origin/<base>`: to catch up it merges the base branch
  `slop-haus-base-ticket-<id>` only, and the "merge `origin/<base>` before you
  merge" step is not yours. `ticket_finish` and the tools that open the change refuse a
  shadow's branch that contains the base branch's commits made after the base commit. If
  yours does, `git fetch origin && git switch -c <branch>-fix <base_commit>`,
  cherry-pick your own commits, and `git push --force-with-lease origin
  HEAD:<branch>`.
- A ticket that reads done to you may still be yours to finish. If `ticket_next` or
  `ticket_get` says the ticket is done through another attempt and yours is a
  live shadow, carry on: finish your attempt as normal (PR from your own
  branch, then `ticket_finish`), and don't stop or release
  because of the ticket's done.
- Keep your scope to what you touch, with `ticket_scope_narrow`. Once the plan
  is settled (typically after reading the code, before the first edit), narrow
  to the files and directories the plan touches: for a new file, name its
  directory, not its parent. Keep anything you're unsure of; getting it back
  means `ticket_scope_extend`, which may be denied. Never narrow below a path
  your branch has already changed, or one you still mean to.
- Claim the narrowest paths you expect: leaf files in hot directories
  (`server/cmd/api/`, `server/internal/ticket/`, `frontend/src/components/`), not the
  directory "in case". In `server/cmd/api/` that is the domain's `mod_<domain>.go`.
- Before `ticket_finish`, `ticket_discuss` or `ticket_release`, narrow again to what the PR touches: take
  `git diff --name-only origin/<base>...HEAD`, coarsen it to directories where
  that is sensible, and drop the rest (`remove`, with `keep` for narrower
  paths inside a directory you drop). A ticket in review holds every path it
  has until a human acts, so a filed scope broader than the diff idles other
  agents.
- If a "MESSAGE FROM THE HUMAN" changes the ticket's direction, say so in your
  final message.
- A `ticket_discuss` handoff is all the next agent gets -- your context
  ends when you finish -- so write where the work stands (branch and the
  pushed commit, what is done and what isn't), what was decided and why,
  and what you would do next. The next agent may be on another checkout:
  commit and push your work to the ticket's branch before `ticket_discuss`
  (and before `ticket_finish`), or it is lost to them.
- When the landing queue hands a ticket back with red checks, don't call `ticket_finish` again
  without a new commit or a cited flake ticket (`red_was_flake`: the id of the open or done `CI flake:`
  ticket): the same head is refused, since each red landing costs two CI runs.
- If slop-haus tells you a human moved or cancelled your ticket, stop and
  finish, reporting that.
- A ticket that ends with nothing to merge (a split parent once all its parts are done or cancelled, an
  investigation that concludes there is nothing to change) closes with `ticket_finish` with `no_change` true
  and a `reason`, which becomes the closing comment. It is refused while the attempt has a change or commits
  beyond the ticket's base commit, or while a ticket it waits on is still open.
- Choose the closing call by what the ticket is waiting on. `ticket_release`
  says what is blocking in a comment:
  - **Another ticket:** `ticket_release` with `blocked_on`. It reopens when
    that ticket is done.
  - **A precondition the queue can't see, or a transient failure** (a deploy
    not yet rolled out, an outside system's state, a flaky dependency):
    `ticket_release` with `retry_after`, a duration that fits how long it
    plausibly takes, e.g. `30m`. Nobody can claim the ticket until then. The
    reason must say what you checked, how (the command or evidence) and what
    would show it's met, so the next agent's check is one step, not a
    rediscovery. A release with neither `blocked_on` nor `retry_after` still
    keeps the ticket out of the queue for a few minutes, and after a few
    such releases in a row the server moves it to discussion itself.
  - **A person's action or decision** (a merge only they can do, approval,
    credentials, a choice): `ticket_discuss`, naming what you need, so the
    ticket waits for their reply.
  - Put a line `[human-only]` in the `ticket_discuss` question to reserve
    the discussion for a person, so the steward never answers it. Use it for
    a decision only the owner can make: priorities, scope, money, security,
    anything irreversible.

Your final message is what the supervisor keeps, so keep it short: the
ticket id, done or released, the branch and PR, the decisions you made, and
anything a human needs to act on.
