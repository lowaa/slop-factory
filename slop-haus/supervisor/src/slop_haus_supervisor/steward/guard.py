"""The steward run's read-only guard.

The steward runs on a fleet host with the person's permissions and reads untrusted ticket text, so its
tools are an allowlist: only STEWARD_ALLOWED_TOOLS and the STEWARD_BUILTIN_TOOLS exist for the run, and
anything else (Bash, Edit/Write, web, an MCP tool not listed) is refused by Claude Code, not by the prompt.
If a check needs a command, the runner runs a fixed one and hands over its output as data.

The deny-list below is kept as a second layer. A prefix deny-list is not a sandbox, so it never stands in
for the allowlist.
"""

from __future__ import annotations

from pathlib import Path

from slop_haus_supervisor.harness.denied import DENIED_TOOLS

FILE_WRITE_TOOLS = ("Edit", "Write", "NotebookEdit", "MultiEdit")

# Bash prefixes that change the repo, the host or a remote. `gh pr diff`, `git log`, `git show` stay allowed.
WRITE_COMMANDS = (
    "git push", "git commit", "git add", "git merge", "git rebase", "git reset", "git checkout", "git switch",
    "git cherry-pick", "git revert", "git rm", "git mv", "git apply", "git am", "git tag", "git branch",
    "git stash", "git clean", "git restore", "git config", "git remote", "git pull",
    "gh pr create", "gh pr merge", "gh pr close", "gh pr edit", "gh pr comment", "gh pr review", "gh pr ready",
    "gh issue", "gh api", "gh release", "gh repo", "gh auth", "gh workflow", "gh run",
    "rm", "mv", "cp", "chmod", "chown", "tee", "dd", "sed -i", "curl", "wget", "sudo", "kill",
)

# A steward claims no ticket, takes no lock and never finishes one: it proposes.
WORKER_TOOLS = (
    *DENIED_TOOLS,
    "mcp__slop-haus__integration_lock",
    "mcp__slop-haus__integration_unlock",
    "mcp__slop-haus__ticket_attach",
    "mcp__slop-haus__ticket_finish",
    "mcp__slop-haus__ticket_release",
    "mcp__slop-haus__ticket_prepare",
    "mcp__slop-haus__ticket_discuss",
    "mcp__slop-haus__ticket_scope_extend",
    "mcp__slop-haus__ticket_scope_narrow",
    "mcp__slop-haus__change_open",
    "mcp__slop-haus__change_merge",
)


STEWARD_BUILTIN_TOOLS = ("Read", "Grep", "Glob")

# The read-only slop-haus tools the steward needs: reading tickets, and proposing for the owner to approve.
STEWARD_ALLOWED_TOOLS = (
    *STEWARD_BUILTIN_TOOLS,
    "mcp__slop-haus__ticket_get",
    "mcp__slop-haus__ticket_attachment_get",
    "mcp__slop-haus__ticket_search",
    "mcp__slop-haus__steward_assess",
    "mcp__slop-haus__steward_draft",
    "mcp__slop-haus__steward_label_discussion",
    "mcp__slop-haus__propose_discussion_answer",
    "mcp__slop-haus__propose_ready",
    "mcp__slop-haus__propose_status",
    "mcp__slop-haus__propose_review_decision",
    "mcp__slop-haus__propose_ticket_edit",
    "mcp__slop-haus__propose_manifest_edit",
)


def steward_denied_tools() -> tuple[str, ...]:
    return (*FILE_WRITE_TOOLS, *(f"Bash({c}:*)" for c in WRITE_COMMANDS), *WORKER_TOOLS)


def scratch_dir(root: Path) -> Path:
    """Where a steward run starts: empty, per tenant root, and the only place it could write if a deny were missed."""
    return root / "state" / "steward-scratch"
