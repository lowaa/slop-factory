"""A supervised run started in a repo below a nested workspace's root still gets the root's
`.claude/settings.json`. Claude Code reads project settings from the working directory only, so
without the supervisor's `--settings` the root's hooks are lost (verified on 2.1.303).

The workspace's settings get a `SessionStart` hook that writes a marker file, which is what the
run's hooks are seen by: slop-haus's own hooks stand down in a supervised run.

The test ends with its own negative run: `--settings` is taken out of the root's copy of the
supervisor and the hook must then not run."""

from __future__ import annotations

import json
import unittest
import uuid

from scripted_model.scenario import Scenario, ToolCall, Turn, Usage
from support.harness_case import excerpt
from support.supervised_case import SupervisedCase
from support.supervisor_case import SupervisorRoot, edit_app
from support.waiting import wait_for

REPO = "repo"
MARKER = "root-session-start-hook-ran"
DONE_PR = "https://github.example/acme/repo/pull/2"


def add_marker_hook(sup: SupervisorRoot) -> None:
    settings = sup.slot_dir / ".claude" / "settings.json"
    config = json.loads(settings.read_text())
    hook = {"hooks": [{"type": "command", "command": f"touch {sup.slot_dir / MARKER}"}]}
    config["hooks"]["SessionStart"].append(hook)
    settings.write_text(json.dumps(config))
    # The supervisor refreshes the slot's managed files, settings.json among them, before each run;
    # a refresh that fails leaves them as they are.
    updater = sup.slot_dir / ".slop-haus" / "update.sh"
    updater.write_text("#!/bin/sh\nexit 1\n")
    updater.chmod(0o755)


class SupervisedNestedWorkspace(SupervisedCase):
    """One slot for both runs: every slot is a device sign-in, and the board allows a user twenty in ten minutes."""

    def new_ticket(self) -> int:
        ticket_id = self.board.create_ticket("Work in the nested repo", "Nothing to edit", [f"{REPO}/e2e/{uuid.uuid4().hex[:8]}/"])
        self.addCleanup(lambda: self.board.request("PATCH", f"/api/tickets/{ticket_id}", {"status": "cancelled"}))
        return ticket_id

    def run_to_review(self, sup: SupervisorRoot, ticket_id: int) -> None:
        a = self.adapter
        self.model.load(Scenario("nested workspace", (
            Turn(tool_calls=(ToolCall(a.mcp_tool("ticket_finish"), {"handoff": "nothing to do", "pr_url": DONE_PR, "request_review": True, "reason": "scripted"}),), usage=Usage(100, 10)),
            Turn(text="All done.", usage=Usage(110, 11)),
        )))
        running = self.run_supervisor(sup, log_name=f"supervisor-{ticket_id}.log")
        wait_for(
            f"ticket #{ticket_id} to be in review",
            lambda: self.board.ticket(ticket_id)["status"] == "review",
            diagnose=lambda: f"model errors: {self.model.errors()}\nsupervisor log:\n{excerpt(running.output(), 2000)}",
        )
        wait_for("the last scripted turn to be played", lambda: self.model.remaining() == 0)
        running.close()

    def test_the_roots_hooks_run_in_a_repo_below_it(self) -> None:
        first, second = self.new_ticket(), self.new_ticket()
        # Held back until the first run is over, so the supervisor claims them one at a time.
        self.board.request("PATCH", f"/api/tickets/{second}", {"status": "preparing"})
        sup = self.supervise(f"nested-{uuid.uuid4().hex[:8]}", (f"ticket-{first}-a1", f"ticket-{second}-a1"), nested_repo=REPO)
        add_marker_hook(sup)
        marker = sup.slot_dir / MARKER

        self.run_to_review(sup, first)

        self.assertTrue(marker.exists(), "the workspace's SessionStart hook did not run in the repo's run")

        # The negative run: without --settings the same run must not see the hook, or the check above proves nothing.
        marker.unlink()
        edit_app(sup, "harness/claude.py", '["--settings", str(settings)]', "[]")
        self.board.request("POST", f"/api/tickets/{second}/ready", {})

        self.run_to_review(sup, second)

        self.assertFalse(marker.exists(), "the hook ran without --settings: the check above does not test it")


if __name__ == "__main__":
    unittest.main()
