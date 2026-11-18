"""A Remote Control the owner adds on the board and the supervisor runs, the main path of
Remote Control: the supervisor, signed in with `login`, reads the board's desired
state, enrols the host, makes its workspace and starts it, and the owner talks to it from the Remote
Control tab. Only the model's replies are scripted.

Each half had been tested against a fake of the other, and every start of a real host failed (a
console where the lease and the tab need a conductor; a token helper looked for in the wrong place).
This is the one test where they meet.

`test_negative_*` is a test of the suite: the root's copy of the supervisor starts the host outside
its workspace, away from its credential, and the scenario has to fail at the host coming up."""

from __future__ import annotations

import unittest
import uuid
from typing import Any

from scripted_model.scenario import Scenario, Turn, Usage
from support.harness_case import MODEL, OTHER_MODEL, excerpt
from support.supervised_case import SECRETS, SupervisedCase
from support.fault_proxy import FaultProxy
from support.supervisor_case import RunningSupervisor, edit_app, log_in, point_at, set_up_root
from support.waiting import Timeout, wait_for

MESSAGE = "are you there?"
REPLY = "Here, in the supervisor's workspace."
SECOND = "and on the other model?"
CHANGED = "Answered on the model chosen since."


class SupervisedRemoteControl(SupervisedCase):
    def play(self, sabotage: bool = False) -> tuple[dict[str, Any], RunningSupervisor]:
        """Adds the remote control on the board and runs the supervisor until its host serves; the conductor and the run."""
        board, adapter = self.board, self.adapter
        name = f"rc-sup-{uuid.uuid4().hex[:8]}"
        sup = set_up_root(board, self.tmp / "tenant", self.tmp / "supervisor-home")
        # The supervisor's own client sends credentials over plain http to loopback only, so it reaches the board
        # through a proxy here that is never armed.
        proxy = FaultProxy(self.stack.server)
        self.addCleanup(proxy.close)
        point_at(sup, proxy.url)
        supervisor_id = log_in(board, sup, f"box-{uuid.uuid4().hex[:8]}")
        # The host's environment is built from the layered files alone, not the supervisor's.
        sup.secrets.write_text(SECRETS[adapter.name])
        (sup.root / "run.env").write_text(
            "".join(f"{k}={v}\n" for k, v in {"ANTHROPIC_BASE_URL": self.stack.model_server, **adapter.hardening_env()}.items())
        )
        workspace = sup.root / "remote-control-hosts" / name
        adapter.prepare_remote_control(workspace / ".slop-haus" / "conductor" / "claude-config", workspace, self.stack.model_server)
        if sabotage:
            edit_app(sup, "fleet/host_slot.py", '"--workspace", str(directory)', '"--workspace", str(directory.parent)')

        board.save_harness_defaults({adapter.name: adapter.harness_config(MODEL)})
        self.addCleanup(board.save_harness_defaults, {})
        was_on = board.provision_from_board()
        board.set_provision_from_board(True)
        self.addCleanup(board.set_provision_from_board, was_on)
        doc = board.desired_state(supervisor_id)
        # Launch is left to the board's default for a new Remote Control: the supervisor.
        board.put_desired_state(supervisor_id, doc["revision"], [
            *doc["agents"], {"kind": "remote_control", "name": name, "state": "running", "resume_generation": 0},
        ])
        self.model.load(Scenario("supervised remote control", (Turn(text=REPLY, usage=Usage(100, 10)), Turn(text=CHANGED, usage=Usage(110, 11)))))
        running = self.run_supervisor(sup)
        self.running = running
        diagnose = lambda: f"supervisor log:\n{excerpt(running.output(), 3000)}"  # noqa: E731

        conductor = wait_for(
            "the host's agent among the owner's remote controls",
            lambda: next((c for c in board.conductors() if c["name"] == name), None),
            diagnose=lambda: f"remote controls: {[c['name'] for c in board.conductors()]}\n{diagnose()}",
        )
        wait_for("the host to be online", lambda: board.conductor_host(conductor["id"])["online"], diagnose=diagnose)
        return conductor, running

    def test_a_remote_control_added_on_the_board_is_run_by_the_supervisor_and_answers_its_owner(self) -> None:
        board, model = self.board, self.model
        conductor, running = self.play()

        conversation = board.start_conversation(conductor["id"], MESSAGE)["id"]

        events = wait_for(
            "the turn to end",
            lambda: (e := board.conversation_events(conductor["id"], conversation)) and any(x["kind"] == "turn_end" for x in e) and e,
            diagnose=lambda: f"model errors: {model.errors()}\nsupervisor log:\n{excerpt(running.output(), 3000)}",
        )
        self.assertEqual([e["body"] for e in events if e["kind"] == "assistant"][-1], REPLY)
        self.assertEqual(model.errors(), [], f"the harness asked for something the script did not expect:\n{excerpt(running.output())}")
        seen = model.conversation()
        self.assertEqual([r.model for r in seen], [MODEL], "the host did not run on the board's harness config")
        self.assertIn(MESSAGE, seen[0].message_text, "the owner's message did not reach the model")

        # The owner then picks another model on the board: the next message runs on it, in the same session,
        # with the host left running. In this test rather than one of its own, which would cost another
        # supervisor sign-in from the api's per-user device-code limit that the suite shares.
        cid = conductor["id"]
        board.save_agent_harness_config(cid, {self.adapter.name: self.adapter.harness_config(OTHER_MODEL)})
        board.send_message(cid, conversation, SECOND)
        events = self.wait_turns(cid, conversation, 2, running)
        self.assertEqual([r.model for r in model.conversation()], [MODEL, OTHER_MODEL], "the second message ran on the old model")
        self.assertIn(MESSAGE, model.conversation()[1].message_text, "the session was not resumed")
        self.assertEqual([e["body"] for e in events if e["kind"] == "assistant"][-1], CHANGED)

        self.assertNotIn("host exited", running.output(), "the host was restarted along the way")
        self.assertIsNone(running.exit_code(), f"the supervisor exited on its own:\n{excerpt(running.output())}")

    def wait_turns(self, conductor_id: str, conversation: str, count: int, running: RunningSupervisor) -> list[dict[str, Any]]:
        return wait_for(
            f"turn {count} to end",
            lambda: (e := self.board.conversation_events(conductor_id, conversation)) and sum(x["kind"] == "turn_end" for x in e) >= count and e,
            diagnose=lambda: f"model errors: {self.model.errors()}\nsupervisor log:\n{excerpt(running.output(), 3000)}",
        )

    def test_negative_the_host_is_started_outside_its_workspace(self) -> None:
        with self.assertRaises(Timeout) as waited:
            self.play(sabotage=True)
        # Enrolled and listed, so the miss is the host's, not a step before it.
        self.assertIn("the host to be online", str(waited.exception))


if __name__ == "__main__":
    unittest.main()
