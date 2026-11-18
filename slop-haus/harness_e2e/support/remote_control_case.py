"""What a scenario about the Remote Control host starts from, whatever harness
it runs: register a remote control the way the owner does, start
`run_remote_control.sh`, play the first messages of a conversation over the
board's API, and stop the host with a turn in flight.

A harness's own scenario file says what is specific to it: how it is
configured and logged in, what the model should be offered and denied, and what
the owner's own configuration holds that must stay out of the session. The
negative tests of a scenario file are tests of the suite: each breaks one thing
in the checkout's copy of the host and expects the round trip to fail at the
assertion about it, so an assertion that stopped asserting is noticed."""

from __future__ import annotations

import os
import re
import signal
import subprocess
import uuid
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Callable

from scripted_model.scenario import Scenario, Turn, Usage
from support.harness_case import MODEL, OTHER_MODEL, HarnessCase, excerpt
from support.harness_run import HarnessRun
from support.model_client import Recorded
from support.waiting import Timeout, wait_for
from support.supervisor_case import UV_ENV
from support.workspace import Workspace, set_up

REPLY = "The repository is clean."
CHANGED_REPLY = "Answered on the model chosen since."
SECOND_REPLY = "Still here."
FIRST_MESSAGE = "what is the state of this repo?"
SECOND_MESSAGE = "and what was my first question?"
STATUS = "git status"
# The model backgrounds a command, as it does for a dev server or a watcher: it is left running
# when the command that started it returns, and is in the host's remit to end with the session.
BACKGROUNDED = "sleep 600"
FIRST_COMMAND = f"{STATUS} && ({BACKGROUNDED} > /dev/null 2>&1 &)"
THIRD_MESSAGE = "one more thing"
# The turn the stop interrupts: held until the test lets it go, which it never does.
HELD = "held-until-the-host-is-stopped"
# Each turn's token counts differ, so a mix-up shows.
USAGE = [Usage(100, 10), Usage(110, 11), Usage(120, 12), Usage(130, 13), Usage(140, 14)]

Sabotage = Callable[[Workspace], None]


@dataclass
class Played:
    workspace: Workspace
    run: HarnessRun
    conversation_id: str
    seen: list[Recorded]


def edit_installed(workspace: Workspace, name: str, old: str, new: str) -> None:
    """Breaks the workspace's copy of one of its installed scripts: what a
    regression in it would be. An edit that finds nothing to change fails, so a
    script that was rewritten does not turn a negative test into a test of nothing."""
    path = workspace.root / ".slop-haus" / name
    text = path.read_text()
    assert old in text, f"{name} no longer contains {old!r}: update this negative test"
    path.write_text(text.replace(old, new, 1))


def package_sources(workspace: Workspace) -> list[Path]:
    """The Python files of the package the host runs from, as install-app.sh put them."""
    return sorted((workspace.root / ".slop-haus" / "app" / "src").rglob("*.py"))


def edit_host(workspace: Workspace, old: str, new: str) -> None:
    """Breaks the host's installed package, in whichever of its files holds `old`, once."""
    holders = [path for path in package_sources(workspace) if old in path.read_text()]
    assert len(holders) == 1, f"{old!r} is in {len(holders)} of the host's package files, not one: update this negative test"
    holders[0].write_text(holders[0].read_text().replace(old, new, 1))


def git(root: Path, *args: str) -> str:
    return subprocess.run(["git", *args], cwd=root, check=True, capture_output=True, text=True).stdout.strip()


class RemoteControlCase(HarnessCase):
    # What names the harness's process among the processes of the host's session,
    # and what to call it in a failure.
    process_marker = ""
    process_label = ""
    # The host is stopped and started again between the first and second message.
    restarts_between_messages = False

    # -- what a harness's scenario file says --

    def prepare(self, workspace: Workspace, logged_in: bool = True) -> None:
        """Everything but starting the host: the remote control's own harness
        configuration, its login (`run.env`), and what the owner has that must
        stay out of it."""
        raise NotImplementedError

    def running(self) -> list[tuple[int, str]]:
        """The processes of the remote control's harness: it and what it ran."""
        raise NotImplementedError

    def scenario(self, workspace: Workspace) -> Scenario:
        raise NotImplementedError

    def check_first_request(self, request: Recorded) -> None:
        """What the very first request to the model must already show."""

    # -- setup --

    def register(self) -> Workspace:
        """A remote control with a harness config of its own, which selects the scripted model."""
        workspace = set_up(self.board, f"rc-{uuid.uuid4().hex[:8]}", self.tmp / "checkout", remote_control=True)
        self.board.save_agent_harness_config(workspace.agent_id, {self.adapter.name: self.adapter.harness_config(MODEL)})
        return workspace

    # What follows run_remote_control.sh on the command line.
    host_args: list[str] = []

    def start_host(self, workspace: Workspace, log_name: str = "host.log", **kwargs: Any) -> HarnessRun:
        self.addCleanup(self.kill_leftovers)
        run = self.start(
            workspace,
            log_name=log_name,
            # --no-update: these scenarios edit the checkout's installed files (the
            # decoy MCP server in .mcp.json, a deliberately broken host package), and a
            # start that updated or reinstalled first would put the real ones back and
            # test nothing. The update on start is run_remote_control.bats's.
            command=[str(workspace.root / ".slop-haus" / "run_remote_control.sh"), "--no-update", *self.host_args],
            # The login is run.env's, not the process environment's.
            # uv runs the host offline, from the interpreter and cache the image holds.
            base_env={**self.adapter.hardening_env(), **UV_ENV},
            **kwargs,
        )
        self.addCleanup(self.release, run)
        return run

    @staticmethod
    def release(run: HarnessRun) -> None:
        """A host still running at the end of a test is stopped as a service manager would, so it
        tells the board it is gone: killed, it holds its place for a while, and the next host of
        this remote control is refused (a registration serves more than one test)."""
        if run.exit_code() is None:
            run.terminate()
            try:
                run.wait_exit(timeout=40)
            except Timeout:
                pass

    def kill_leftovers(self) -> None:
        """Whatever a failed test leaves running, so it does not outlive the test."""
        for pid, _ in self.running():
            try:
                os.kill(pid, signal.SIGKILL)
            except ProcessLookupError:
                pass

    # -- observation --

    def events(self, conductor_id: str, conversation_id: str) -> list[dict[str, Any]]:
        return self.board.conversation_events(conductor_id, conversation_id)

    def wait_for_turns(self, conductor_id: str, conversation_id: str, count: int, run: HarnessRun) -> list[dict[str, Any]]:
        def done() -> list[dict[str, Any]] | None:
            events = self.events(conductor_id, conversation_id)
            return events if sum(e["kind"] == "turn_end" for e in events) >= count else None

        return wait_for(
            f"turn {count} to end",
            done,
            diagnose=lambda: (
                f"events: {[(e['kind'], e['body'][:80]) for e in self.events(conductor_id, conversation_id)]}\n"
                f"model requests: {len(self.model.conversation())}, errors: {self.model.errors()}\nhost output:\n{excerpt(run.output())}"
            ),
        )

    def usage_row(self, name: str) -> dict[str, Any] | None:
        return next((a for a in self.board.llm_usage()["agents"] if a["name"] == name), None)

    # -- the conversation --

    def play(self, sabotage: Sabotage | None = None) -> Played:
        """Registers a remote control, starts it, and plays the first two messages
        of a conversation, asserting on the board as it goes."""
        board, model = self.board, self.model
        # Not the model the remote control is configured with: its own config wins, or MODEL is not what is asked for.
        board.save_harness_defaults({self.adapter.name: self.adapter.harness_config("tenant-default-model")})
        self.addCleanup(board.save_harness_defaults, {})
        workspace = self.register()
        self.prepare(workspace)
        if sabotage:
            sabotage(workspace)
        model.load(self.scenario(workspace))
        run = self.start_host(workspace)
        conductor_id = workspace.agent_id
        before = self.usage_row(workspace.name)

        conversation_id = board.start_conversation(conductor_id, FIRST_MESSAGE)["id"]
        wait_for(
            "the first model request",
            lambda: model.conversation(),
            diagnose=lambda: f"model errors: {model.errors()}\nhost output:\n{excerpt(run.output())}",
        )
        self.check_first_request(model.conversation()[0])
        events = self.wait_for_turns(conductor_id, conversation_id, 1, run)
        self.assertEqual(model.errors(), [], f"the harness asked for something the script did not expect:\n{excerpt(run.output())}")

        # -- the board after the first turn --
        self.assertEqual([e["body"] for e in events if e["kind"] == "assistant"][-1], REPLY, "the conversation does not show the reply")
        detail = board.conversation(conductor_id, conversation_id)
        self.assertEqual(detail["state"], "ready")
        self.assertIsNotNone(detail["repo"], "no repo state was reported after the turn")
        self.assertEqual((detail["repo"]["branch"], detail["repo"]["head"]), ("main", git(workspace.root, "rev-parse", "HEAD")))
        # What the board held for this remote control before the conversation: usage is per
        # agent, and a registration may serve more than one test.
        spent_before = (before["input_tokens"], before["output_tokens"], before["requests"]) if before else (0, 0, 0)
        first = USAGE[:3]
        row = wait_for(
            "the usage of the first turn to reach the board, attributed to the remote control",
            lambda: next(
                (a for a in [self.usage_row(workspace.name)] if a and a["input_tokens"] == spent_before[0] + sum(u.input_tokens for u in first)),
                None,
            ),
            diagnose=lambda: f"board usage: {board.llm_usage()['agents']}\nhost output:\n{excerpt(run.output())}",
        )
        self.assertEqual(
            (row["output_tokens"], row["requests"]),
            (spent_before[1] + sum(u.output_tokens for u in first), spent_before[2] + 3),
        )
        # The board's model rows are the whole stack's, so this alone would not
        # tell this remote control's model; the requests below do.
        models = board.llm_usage()["models"]
        self.assertTrue(any(m["model"] == MODEL for m in models), f"no usage was recorded for the configured model {MODEL!r}: {models}")

        if self.restarts_between_messages:
            run = self.restart(workspace, run)

        # -- a second message --
        board.send_message(conductor_id, conversation_id, SECOND_MESSAGE)
        events = self.wait_for_turns(conductor_id, conversation_id, 2, run)
        self.assertEqual([e["body"] for e in events if e["kind"] == "assistant"][-1], SECOND_REPLY)
        self.assertEqual(model.errors(), [])
        seen = model.conversation()
        self.assertEqual(len(seen), 4, f"requests: {len(seen)}")
        self.assertEqual(
            [r.model for r in seen], [MODEL] * 4,
            "the sessions ran on another model than the remote control's own harness config (the tenant defaults win?)",
        )
        return Played(workspace, run, conversation_id, seen)

    def config_change(self, sabotage: Sabotage | None = None) -> None:
        """The owner changes the remote control's model on the board while its session is live, and
        sends the next message: it is answered on the new model, in the same session, with no restart
        of the host by anyone."""
        board, model = self.board, self.model
        workspace = self.register()
        # A harness's scenarios may share one registration: it starts on MODEL whatever an earlier test left.
        starting = {self.adapter.name: self.adapter.harness_config(MODEL)}
        board.save_agent_harness_config(workspace.agent_id, starting)
        self.addCleanup(board.save_agent_harness_config, workspace.agent_id, starting)
        self.prepare(workspace)
        if sabotage:
            sabotage(workspace)
        model.load(Scenario("a model changed on the board", (Turn(text=REPLY, usage=USAGE[0]), Turn(text=CHANGED_REPLY, usage=USAGE[1]))))
        run = self.start_host(workspace)
        conductor_id = workspace.agent_id
        conversation_id = board.start_conversation(conductor_id, FIRST_MESSAGE)["id"]
        self.wait_for_turns(conductor_id, conversation_id, 1, run)
        # Said apart from the failure the negative test expects, so a session that never started on MODEL cannot pass for one.
        self.assertEqual([r.model for r in model.conversation()], [MODEL], "precondition: the first message ran on another model than the one configured")

        board.save_agent_harness_config(conductor_id, {self.adapter.name: self.adapter.harness_config(OTHER_MODEL)})
        board.send_message(conductor_id, conversation_id, SECOND_MESSAGE)

        events = self.wait_for_turns(conductor_id, conversation_id, 2, run)
        self.assertEqual(model.errors(), [], f"the harness asked for something the script did not expect:\n{excerpt(run.output())}")
        seen = model.conversation()
        self.assertEqual([r.model for r in seen], [MODEL, OTHER_MODEL], "the second message ran on the model the session started with")
        for earlier in (FIRST_MESSAGE, REPLY):
            self.assertIn(earlier, seen[1].message_text, f"the session was not resumed: the second request did not carry {earlier!r}")
        self.assertEqual([e["body"] for e in events if e["kind"] == "assistant"][-1], CHANGED_REPLY)
        self.assertIsNone(run.exit_code(), f"the host exited:\n{excerpt(run.output())}")

    def stop_host(self, run: HarnessRun, backgrounded: bool = True) -> None:
        """SIGTERM, as a service manager sends it: the host ends its harness, and what the model's
        first command left running, and exits. `backgrounded` says that command is still running
        (a host that was started again has none: the model has not run it in that session)."""
        if backgrounded:
            running = [command for _, command in self.running()]
            self.assertTrue(
                any(BACKGROUNDED in c for c in running),
                f"the command the model backgrounded is not running, so nothing being gone would prove nothing: {running}",
            )
        run.terminate()
        self.assertEqual(run.wait_exit(timeout=40), 0, f"the host did not stop cleanly on SIGTERM:\n{run.output()}")
        wait_for(
            f"the harness to be gone (an orphaned {self.process_label} process)",
            lambda: not any(self.process_marker in c for _, c in self.running()),
            timeout=10,
            diagnose=lambda: f"still running: {self.running()}",
        )
        wait_for(
            "the command the model backgrounded to be gone (the host's group signal cannot reach it: it runs in a session of its own)",
            lambda: not self.running(),
            timeout=10,
            diagnose=lambda: f"still running: {self.running()}",
        )

    def restart(self, workspace: Workspace, run: HarnessRun) -> HarnessRun:
        """The host is stopped and started again."""
        self.stop_host(run)
        return self.start_host(workspace, log_name="host-restarted.log")

    def stop(self, played: Played) -> None:
        """SIGTERM with a turn in flight: the harness is busy, so only the host can end it."""
        board, model, run = self.board, self.model, played.run
        board.send_message(played.workspace.agent_id, played.conversation_id, THIRD_MESSAGE)
        wait_for(
            "the third turn's request to be held at its gate",
            lambda: len(model.conversation()) == 5,
            diagnose=lambda: f"requests: {len(model.conversation())}, errors: {model.errors()}\nhost output:\n{excerpt(run.output())}",
        )
        running = [command for _, command in self.running()]
        self.assertTrue(
            any(self.process_marker in c for c in running),
            f"no {self.process_label} process is running, so nothing being gone would prove nothing: {running}",
        )
        self.stop_host(run, backgrounded=not self.restarts_between_messages)

    # Breaks the host's ending of what the harness started beyond its process group: it exits
    # and leaves a backgrounded command running.
    LEAVES_BACKGROUNDED = (
        ("        self._tree.signal(signal.SIGTERM)\n", "        pass\n"),
        ("        self._tree.kill_all()\n", "        pass\n"),
    )

    def expect_failure(self, run: Callable[[], object], failure: str) -> None:
        with self.assertRaisesRegex(AssertionError, re.escape(failure)) as caught:
            run()
        # What the failure says: `test.sh -rP` shows it, which is what a pull request that
        # changes these assertions should quote.
        print(f"EXPECTED FAILURE: {str(caught.exception).splitlines()[0]}")
