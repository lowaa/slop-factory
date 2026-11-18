"""A supervised worker takes a ticket from the board to review, on the real harness, against a
scripted model: the supervisor's claim, its `claude -p` command, MCP connection and usage report
are real; only the model's replies are scripted.

It stops with the ticket still held, and the supervisor's nudge must send it back to work.

`test_negative_*` is a test of the suite: the nudge is taken out of the root's copy of the
supervisor and the scenario has to fail at the assertion about it."""

from __future__ import annotations

import unittest
import uuid

from scripted_model.scenario import Scenario, ToolCall, Turn, Usage
from support.harness_case import excerpt
from support.supervised_case import SupervisedCase
from support.supervisor_case import edit_app
from support.waiting import Timeout, wait_for

EDIT = "echo '# edited by the supervised worker' >> src/uploader.py"
DONE_SUMMARY = "edited src/uploader.py"
DONE_PR = "https://github.example/acme/repo/pull/1"
# Each turn's token counts differ, so a mix-up shows.
USAGE = [Usage(100, 10), Usage(120, 12), Usage(130, 14), Usage(140, 16)]
# What the supervisor says to a model that stopped without a closing call (policy.NUDGE_PROMPT).
NUDGE = "You stopped without finishing the ticket"
EARLY_STOP = "I believe I am finished."


class SupervisedTicketToReview(SupervisedCase):
    runs_on_pi = True

    def scenario(self) -> Scenario:
        a = self.adapter
        return Scenario(
            "supervised ticket to review",
            (
                Turn(tool_calls=(ToolCall(a.bash_tool, {"command": EDIT, "description": "edit"}),), usage=USAGE[0]),
                # Stops with the ticket still held: the supervisor must send it back.
                Turn(text=EARLY_STOP, usage=USAGE[1]),
                Turn(tool_calls=(ToolCall(a.mcp_tool("ticket_finish"), {"handoff": DONE_SUMMARY, "pr_url": DONE_PR, "request_review": True, "reason": "the scripted worker hands its change over"}),), usage=USAGE[2]),
                Turn(text="All done.", usage=USAGE[3]),
            ),
        )

    def play(self, sabotage: bool = False) -> tuple[str, int]:
        board, model = self.board, self.model
        name = f"supervised-{uuid.uuid4().hex[:8]}"
        # A ticket in review keeps its scope until a human acts, and this runs once per harness
        # mode against one board: a scope of its own, and cancelled after.
        ticket_id = board.create_ticket("Add a note to the uploader", "Append a comment to src/uploader.py", [f"e2e/{uuid.uuid4().hex[:8]}/"])
        self.addCleanup(lambda: board.request("PATCH", f"/api/tickets/{ticket_id}", {"status": "cancelled"}))
        sup = self.supervise(name, (f"ticket-{ticket_id}-a1",))
        if sabotage:
            edit_app(sup, "fleet/policy.py", NUDGE, "Carry on")
        self.sup = sup
        model.load(self.scenario())
        running = self.run_supervisor(sup)
        self.running = running

        wait_for(
            f"ticket #{ticket_id} to be in review",
            lambda: board.ticket(ticket_id)["status"] == "review",
            diagnose=lambda: (
                f"{len(model.conversation())} model request(s) so far, {model.remaining()} scripted turn(s) unplayed; "
                f"model errors: {model.errors()}\nsupervisor log:\n{excerpt(running.output(), 2000)}"
            ),
        )
        wait_for("the last scripted turn to be played", lambda: model.remaining() == 0)
        return name, ticket_id

    def test_ticket_claimed_edited_and_handed_to_review(self) -> None:
        board, model = self.board, self.model
        name, ticket_id = self.play()
        self.assertIsNone(self.running.exit_code(), f"the supervisor exited on its own:\n{excerpt(self.running.output())}")
        self.assertEqual(model.errors(), [], f"the harness asked for something the script did not expect:\n{excerpt(self.running.output())}")

        # -- the board --
        self.assertEqual(board.handoffs(ticket_id), [DONE_SUMMARY], "ticket_finish's handoff should be recorded on the ticket")
        agent = board.agent(name)
        self.assertIsNone(agent["lease"])
        self.assertEqual((self.sup.slot_dir / "src" / "uploader.py").read_text().splitlines()[-1], "# edited by the supervised worker")
        # The conversation's usage is reported in full, so the board ends up with every scripted
        # request. Not the exact token counts: a run's modelUsage also counts requests the harness
        # makes for itself.
        totals = wait_for(
            "the usage of the run to reach the board",
            lambda: next((a for a in board.llm_usage()["agents"] if a["name"] == name and a["requests"] >= len(USAGE)), None),
            diagnose=lambda: f"board usage: {board.llm_usage()['agents']}",
        )
        self.assertGreaterEqual(totals["input_tokens"], sum(u.input_tokens for u in USAGE))

        # -- what the model was shown --
        seen = model.conversation()
        self.assertEqual(len(seen), len(USAGE))
        first = seen[0]
        if self.adapter.name == "pi":
            self.assertEqual(self.adapter.thinking_in(first.raw), self.adapter.thinking, "the root's default variant did not reach pi")
            self.assertIn("container or VM", self.running.output(), "a pi slot's supervisor did not warn")
        for tool in ("ticket_finish", "checkin"):
            self.assertIn(self.adapter.mcp_tool(tool), first.tool_names, "slop-haus's MCP tools were not offered to the harness")
        self.assert_seen(f"# slop-haus agent: {name}", first.system + first.message_text, "the briefing did not reach the model as its system prompt")
        self.assert_seen(f"Work ticket #{ticket_id}", first.message_text, "the supervisor's prompt did not name the ticket")
        # The early stop, with the ticket still held, came back as the supervisor's nudge on the same session.
        self.assert_seen(NUDGE, seen[2].message_text, "the supervisor's nudge did not reach the model")
        self.assert_seen(EARLY_STOP, seen[2].message_text, "the nudge started a new conversation instead of resuming the session")
        self.assertNotIn(NUDGE, seen[1].message_text)

    def test_negative_a_supervisor_that_does_not_nudge_fails_at_the_nudge_assertion(self) -> None:
        with self.assertRaises(AssertionError) as failed:
            self.play(sabotage=True)
            seen = self.model.conversation()
            self.assert_seen(NUDGE, seen[2].message_text, "the supervisor's nudge did not reach the model")
        self.assertNotIsInstance(failed.exception, Timeout, "it should have run to the end and failed on what the model was shown")
        self.assertIn("the supervisor's nudge did not reach the model", str(failed.exception))


if __name__ == "__main__":
    unittest.main()
