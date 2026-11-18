from __future__ import annotations

# Claiming belongs to the supervisor. Monitor is there because a run that starts a background
# watcher and ends its turn either exits or lingers looking alive while holding the integration lock.
# The one list for every harness and the steward, so they cannot drift.
DENIED_TOOLS = (
    "mcp__slop-haus__ticket_next",
    "Monitor",
)
