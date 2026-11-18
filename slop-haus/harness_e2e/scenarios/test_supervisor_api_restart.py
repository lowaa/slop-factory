"""The supervisor takes a ticket to review while the api restarts under it, with no human action.

The real supervisor package (set up by the board's own setup script, a fresh slot signed in by
`slot add`) runs the real `claude -p` against the scripted model. A run ends while the api is
down; the supervisor's calls ride out the outage and it resumes the session when the api is
back. The fault proxy stands in for the restart: the frontend's nginx answers 502 while the api
is down, and the proxy answers the same. Claude Code only: the supervisor wires no other harness.

`test_negative_*` is a test of the suite: the supervisor's retrying of the api is taken out of the
root's copy of it and the scenario has to fail at the assertion about it."""

from __future__ import annotations

import unittest
import uuid
from collections.abc import Callable

from harnesses.claude import ClaudeCode
from scripted_model.scenario import Scenario, ToolCall, Turn, Usage
from support.fault_proxy import FaultProxy
from support.harness_case import excerpt
from support.supervised_case import SupervisedCase
from support.supervisor_case import RunningSupervisor, SupervisorRoot, edit_app
from support.waiting import Timeout, wait_for

EDIT = "echo '# edited by the supervised worker' >> src/uploader.py"
DONE_SUMMARY = "edited src/uploader.py"
DONE_PR = "https://github.example/acme/repo/pull/1"
DOWN_HOLD = "hold-the-run-until-the-api-is-down"
Sabotage = Callable[[SupervisorRoot], None]
# Each turn's token counts differ, so a mix-up shows.
USAGE = [Usage(100, 10), Usage(110, 11), Usage(120, 12), Usage(130, 13)]
# What the supervisor says to a model that stopped without a closing call (policy.NUDGE_PROMPT).
NUDGE = "You stopped without finishing the ticket"
# How many refused attempts of the supervisor's own calls prove it was retrying through the outage.
RETRIES_SEEN = 2


class SupervisorApiRestart(SupervisedCase):
    def setUp(self) -> None:
        super().setUp()
        self.proxy = FaultProxy(self.stack.server)
        self.addCleanup(self.proxy.close)

    def scenario(self, ticket_id: int) -> Scenario:
        tool = ClaudeCode().mcp_tool
        return Scenario(
            "supervisor survives an api restart",
            (
                Turn(tool_calls=(ToolCall("Bash", {"command": EDIT, "description": "edit"}),), usage=USAGE[0]),
                # Ends the turn with the ticket still held, while the test has the api down.
                Turn(text="Edited; stopping here.", usage=USAGE[1], gate=DOWN_HOLD),
                Turn(tool_calls=(ToolCall(tool("ticket_finish"), {"handoff": DONE_SUMMARY, "pr_url": DONE_PR, "request_review": True, "reason": "the scripted worker hands its change over"}),), usage=USAGE[2]),
                Turn(text="All done.", usage=USAGE[3]),
            ),
        )

    def play(self, sabotage: Sabotage | None = None) -> tuple[RunningSupervisor, str, int, SupervisorRoot]:
        board, model = self.board, self.model
        name = f"supervised-{uuid.uuid4().hex[:8]}"
        # A scope of its own: a run left holding its ticket (the negative one) must not keep this one from being claimed.
        ticket_id = board.create_ticket("Add a note to the uploader", "Append a comment to src/uploader.py", [f"e2e/{uuid.uuid4().hex[:8]}/"])
        sup = self.supervise(name, (f"ticket-{ticket_id}-a1",), server=self.proxy.url)
        if sabotage:
            sabotage(sup)
        model.load(self.scenario(ticket_id))
        running = self.run_supervisor(sup)

        def diagnose() -> str:
            return (
                f"{len(model.conversation())} model request(s), {model.remaining()} scripted turn(s) unplayed, "
                f"model errors: {model.errors()}\napi traffic of the supervisor:\n{self.proxy.summary()}\n"
                f"supervisor log:\n{excerpt(running.output(), 2000)}"
            )

        # The first run is at its last turn, held: the supervisor has claimed the ticket and
        # started it, and the model has done its edit.
        wait_for("the first run to reach the held turn", lambda: len(model.conversation()) == 2, diagnose=diagnose)
        self.assertEqual(board.ticket(ticket_id)["status"], "in_progress")

        # The api goes down; the run's turn ends while it is, so the supervisor's next calls fail.
        self.proxy.set_down(True)
        model.open_gate(DOWN_HOLD)
        wait_for(
            "the supervisor to be refused its calls while the api is down, and to keep trying",
            lambda: len([e for e in self.proxy.fired() if e.path == "/api/worker/state"]) >= RETRIES_SEEN,
            diagnose=diagnose,
        )
        self.assertNotEqual(board.ticket(ticket_id)["status"], "review")
        self.proxy.set_down(False)

        # Back up, and nobody has touched the supervisor: the ticket reaches review.
        wait_for(
            f"ticket #{ticket_id} to be in review by the supervisor after the api came back",
            lambda: board.ticket(ticket_id)["status"] == "review",
            diagnose=diagnose,
        )
        return running, name, ticket_id, sup

    def test_the_supervisor_rides_out_an_api_restart_and_finishes_the_ticket(self) -> None:
        running, name, ticket_id, sup = self.play()
        board, model = self.board, self.model
        self.assertIsNone(running.exit_code(), f"the supervisor exited on its own:\n{excerpt(running.output())}")
        self.assertEqual(model.errors(), [])
        wait_for("the last scripted turn to be played", lambda: model.remaining() == 0)

        agent = board.agent(name)
        self.assertIsNone(agent["lease"])
        # The session the second run resumed was recorded on the attempt (only a claim response shows it).
        self.assertIn(("POST", "/api/claims/session", 204), [(e.method, e.path, e.status) for e in self.proxy.events()])
        self.assertEqual((sup.slot_dir / "src" / "uploader.py").read_text().splitlines()[-1], "# edited by the supervised worker")
        # The conversation's usage is reported in full (one record per report), so the second run's report
        # carries the first run's too: this shows the board ends up with every scripted request, not that
        # each run's own report got through. Not the exact token counts: a run's modelUsage also counts
        # tokens of requests the harness makes for itself.
        totals = wait_for(
            "the usage of both runs to reach the board",
            lambda: next((a for a in board.llm_usage()["agents"] if a["name"] == name and a["requests"] >= len(USAGE)), None),
            diagnose=lambda: f"board usage: {board.llm_usage()['agents']}",
        )
        self.assertGreaterEqual(totals["input_tokens"], sum(u.input_tokens for u in USAGE))
        self.assertEqual(board.handoffs(ticket_id), [DONE_SUMMARY])

        seen = self.model.conversation()
        self.assertEqual(len(seen), len(USAGE))
        for tool in ("ticket_finish", "checkin"):
            self.assertIn(ClaudeCode().mcp_tool(tool), seen[0].tool_names)
        # The api was refused at least twice and answered after: the supervisor waited, it did not give up.
        self.assertIn("trying again", running.output())
        # The second run is the first session resumed with the supervisor's nudge, not a new conversation.
        self.assertIn(NUDGE, seen[2].message_text)
        self.assertIn("Edited; stopping here.", seen[2].message_text)

    def test_negative_a_supervisor_that_does_not_retry_the_api_does_not_finish(self) -> None:
        def no_retry(sup: SupervisorRoot) -> None:
            edit_app(sup, "slophaus/http_client.py", "attempts: int | None = None,\n    ) -> dict", "attempts: int | None = 1,\n    ) -> dict")

        with self.assertRaises(Timeout) as failed:
            self.play(sabotage=no_retry)
        # Which wait gives up depends on how far it got; both are about riding out the outage.
        self.assertIn("the supervisor to", str(failed.exception))


if __name__ == "__main__":
    unittest.main()
