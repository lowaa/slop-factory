"""The Remote Control on the real pi: `run_remote_control.sh --harness pi`, the
host (the supervisor package's `slop-haus-remote-control`) it runs, and the `pi --mode rpc` process the host
starts for a conversation, against a real slop-haus stack and a scripted model.

Started the way the owner does it: a remote control registered
on the board, its setup script run in a fresh checkout, a harness config (pi's
provider, model and thinking) set on the board, and the provider configured in
the remote control's own pi agent directory (`.slop-haus/conductor/pi-agent`,
`PI_CODING_AGENT_DIR`) with its key in `run.env`.

One registration serves the whole class: each costs the board's device-code
and client-registration limits, which the whole suite shares. Every test starts
from the checkout as setup left it.

The negative tests are tests of this suite: each breaks one thing in the
checkout's copy of the host (or of the extension), and expects the round trip to
fail at the assertion about it, so an assertion that stopped asserting is noticed."""

from __future__ import annotations

import ast
import json
import os
import shutil
import tempfile
import unittest
import uuid
from pathlib import Path

from harnesses.pi import PROVIDER
from scripted_model.scenario import Scenario, ToolCall, Turn
from support import processes
from support.harness_case import MODEL, redacted
from support.harness_run import HarnessRun
from support.model_client import Recorded
from support.remote_control_case import (
    FIRST_COMMAND, FIRST_MESSAGE, HELD, REPLY, SECOND_MESSAGE, SECOND_REPLY, STATUS, USAGE, Played, RemoteControlCase, Sabotage, edit_host, package_sources,
    edit_installed,
)
from support.waiting import wait_for
from support.workspace import Workspace, set_up
from support.harness_case import excerpt

# What the owner's own pi agent directory (~/.pi/agent) holds, and which must stay out of the session.
OWNERS_APPEND_SYSTEM = "OWNERS-PI-APPEND-SYSTEM-MARKER: always answer in verse."
PROJECT_APPEND_SYSTEM = "PROJECT-PI-APPEND-SYSTEM-MARKER: the repo's .pi says so."
REFUSAL = "credentials are not for the model"
# Files a planted extension writes when pi loads it.
EXTENSIONS = ("owner", "agent_dir", "project", "settings")


def host_constants(path: Path, *names: str) -> set[str]:
    """The tuples of strings the host's source assigns to these names, read without running it."""
    found: set[str] = set()
    for node in ast.parse(path.read_text()).body:
        if isinstance(node, ast.Assign) and any(isinstance(t, ast.Name) and t.id in names for t in node.targets):
            found |= set(ast.literal_eval(node.value))
    return found


@unittest.skipUnless(os.environ.get("HARNESS") == "pi", "the Remote Control host's pi path")
class PiRemoteControl(RemoteControlCase):
    process_marker = "pi"
    process_label = "pi"
    # Pi's session totals outlive the process, which is what the host's persisted baseline is for.
    restarts_between_messages = True

    # -- setup: one registration, each test from the checkout as setup left it --

    @classmethod
    def setUpClass(cls) -> None:
        super().setUpClass()
        cls.root_dir = Path(tempfile.mkdtemp(prefix="harness-e2e-rc-pi-"))
        cls.workspace = set_up(cls.board, f"rc-pi-{uuid.uuid4().hex[:8]}", cls.root_dir / "checkout", remote_control=True)
        cls.board.save_agent_harness_config(cls.workspace.agent_id, {cls.adapter.name: cls.adapter.harness_config(MODEL)})
        slop_haus = cls.workspace.root / ".slop-haus"
        cls.pristine = {
            path.relative_to(slop_haus).as_posix(): path.read_bytes()
            for path in [*package_sources(cls.workspace), slop_haus / "pi" / "slop-haus.ts"]
        }

    def register(self) -> Workspace:
        """The class's remote control, with what an earlier test did to its checkout undone."""
        root = self.workspace.root
        for rel, content in self.pristine.items():
            (root / ".slop-haus" / rel).write_bytes(content)
        for leftover in (root / ".slop-haus" / "run.env", root / ".slop-haus" / "conductor", root / ".pi"):
            if leftover.is_dir():
                shutil.rmtree(leftover)
            elif leftover.exists():
                leftover.unlink()
        return self.workspace

    @property
    def agent_dir(self) -> Path:
        return self.workspace.root / ".slop-haus" / "conductor" / "pi-agent"

    @property
    def markers(self) -> Path:
        return self.tmp / "markers"

    def plant_extension(self, directory: Path, name: str) -> None:
        """An extension that says it was loaded: the extension API has nothing a
        test could see of one that was kept out, except what it does."""
        directory.mkdir(parents=True, exist_ok=True)
        self.markers.mkdir(exist_ok=True)
        (directory / f"{name}.ts").write_text(
            f'import {{ writeFileSync }} from "node:fs";\n'
            f'export default function () {{ writeFileSync({json.dumps(str(self.markers / name))}, "loaded"); }}\n'
        )

    def prepare(self, workspace: Workspace, logged_in: bool = True, provider: bool = True) -> None:
        """Everything but starting the host: the remote control's own agent
        directory with the scripted provider, its key (`run.env`), and what the
        owner has, in the home and in the repo, that must stay out of it."""
        adapter, root = self.adapter, workspace.root
        if provider:
            adapter.prepare_remote_control(self.agent_dir, root, self.stack.model_server)
        self.agent_dir.mkdir(parents=True, exist_ok=True)
        # An owner who has trusted this repo before: only --no-approve keeps its .pi out of the session.
        settings_path = self.agent_dir / "settings.json"
        settings = json.loads(settings_path.read_text()) if settings_path.exists() else {}
        settings["defaultProjectTrust"] = "always"
        settings_path.write_text(json.dumps(settings))
        login = adapter.login_env(self.stack.model_server) if logged_in else {}
        (root / ".slop-haus" / "run.env").write_text("".join(f"{k}={v}\n" for k, v in login.items()))

        # Extensions pi would discover, and the instructions it would load, were
        # the host to let it: in the owner's home, in the host's own directory
        # (which the owner may use), in the repo's .pi and in its settings.
        owners = self.tmp / "home" / ".pi" / "agent"
        self.plant_extension(owners / "extensions", "owner")
        (owners / "APPEND_SYSTEM.md").write_text(OWNERS_APPEND_SYSTEM)
        self.plant_extension(self.agent_dir / "extensions", "agent_dir")
        self.plant_extension(root / ".pi" / "extensions", "project")
        self.plant_extension(self.tmp / "elsewhere", "settings")
        (root / ".pi" / "settings.json").write_text(json.dumps({"extensions": [str(self.tmp / "elsewhere" / "settings.ts")]}))
        (root / ".pi" / "APPEND_SYSTEM.md").write_text(PROJECT_APPEND_SYSTEM)

    def running(self) -> list[tuple[int, str]]:
        """The processes of the remote control's pi: it and what it ran."""
        return processes.with_environment("PI_CODING_AGENT_DIR", str(self.agent_dir))

    host_args = ["--harness", "pi"]

    def start_host(self, workspace: Workspace, log_name: str = "host.log") -> HarnessRun:
        # What the owner's home holds is the test's to say, not the adapter's.
        return super().start_host(workspace, log_name, prepare_home=False, extra_env={"PATH": f"{self.shim_pi()}:{os.environ['PATH']}"})

    # pi leaves when its stdin closes, so a host that did not end it would still find it gone. A pi
    # that does not (kept alive by a pipe of its own) is what shows the host ending it itself.
    pi_ignores_eof = False

    def shim_pi(self) -> Path:
        """A `pi` in front of the real one that writes down its arguments and runs it: pi
        retitles its process, so /proc has only "pi" to say of how the host started it."""
        directory = self.tmp / "shim"
        if not directory.exists():
            directory.mkdir()
            real = shutil.which("pi")
            assert real, "pi is not on PATH"
            run = f"(cat; exec sleep 3600) | {real} \"$@\"" if self.pi_ignores_eof else f"exec {real} \"$@\""
            shim = directory / "pi"
            shim.write_text(f'#!/bin/sh\nprintf \'%s\\0\' "$@" > "{directory}/$$.argv"\n{run}\n')
            shim.chmod(0o755)
        return directory

    # -- scenario --

    def scenario(self, workspace: Workspace) -> Scenario:
        a = self.adapter
        return Scenario(
            "pi remote control round trip",
            (
                Turn(tool_calls=(ToolCall(a.bash_tool, {"command": FIRST_COMMAND}),), usage=USAGE[0]),
                Turn(tool_calls=(ToolCall("read", {"path": ".slop-haus/credentials.json"}),), usage=USAGE[1]),
                Turn(text=REPLY, usage=USAGE[2]),
                Turn(text=SECOND_REPLY, usage=USAGE[3]),
                Turn(text="never sent", usage=USAGE[4], gate=HELD),
            ),
        )

    # -- assertions on what the host started, the model was sent and the board shows --

    def pi_argv(self) -> list[str]:
        pids = [pid for pid, command in self.running() if command == "pi"]
        self.assertEqual(len(pids), 1, f"expected one pi process of the remote control, found {len(pids)}: {self.running()}")
        return (self.tmp / "shim" / f"{pids[0]}.argv").read_bytes().decode().split("\0")[:-1]

    @staticmethod
    def flag(argv: list[str], name: str) -> str | None:
        return argv[argv.index(name) + 1] if name in argv else None

    def host_tools(self) -> set[str]:
        """What the host's allowlist is, from the host as setup installed it."""
        return host_constants(self.workspace.root / ".slop-haus" / "app" / "src" / "slop_haus_supervisor" / "harness" / "pi.py", "PI_TOOLS", "SLOP_HAUS_TOOLS")

    def assert_flags(self, conversation_id: str) -> None:
        argv = self.pi_argv()
        a = self.adapter
        for flag, wanted in (("--provider", PROVIDER), ("--model", MODEL), ("--thinking", a.thinking), ("--session-id", conversation_id)):
            self.assertEqual(self.flag(argv, flag), wanted, f"the host did not start pi with {flag} {wanted} (the board's pi settings did not arrive as flags): {argv[:12]}")

    def assert_the_model_was_offered_only_the_allowlist(self, seen: list[Recorded], expected: set[str]) -> None:
        for request in seen:
            offered = set(request.tool_names)
            self.assertTrue(
                offered == expected,
                f"the tools offered to the model are not the host's allowlist plus pi's built-ins: missing {sorted(expected - offered)}, unexpected {sorted(offered - expected)}",
            )

    def assert_thinking_reached_the_model(self, seen: list[Recorded]) -> None:
        sent = {self.adapter.thinking_in(r.raw) for r in seen}
        self.assertTrue(sent == {self.adapter.thinking}, f"the thinking setting did not reach the model request: it was sent {sorted(sent)}")

    def assert_nothing_of_the_owner_loaded(self, seen: list[Recorded]) -> None:
        for request in seen:
            text = request.system + request.message_text
            self.assertFalse(OWNERS_APPEND_SYSTEM in text, "the owner's own ~/.pi/agent reached the session (PI_CODING_AGENT_DIR is not the remote control's own)")

    def assert_no_extension_loaded(self, names: tuple[str, ...]) -> None:
        loaded = sorted(n for n in names if (self.markers / n).exists())
        self.assertFalse(loaded, f"extensions that --no-extensions should keep out were loaded: {loaded}")

    def assert_the_repos_pi_stayed_out(self, seen: list[Recorded]) -> None:
        self.assertFalse((self.markers / "project").exists() or (self.markers / "settings").exists(), "the repo's .pi extension or setting loaded (--no-approve)")
        for request in seen:
            self.assertFalse(PROJECT_APPEND_SYSTEM in request.system, "the repo's .pi/APPEND_SYSTEM.md reached the session (--no-approve)")

    def assert_credentials_read_refused(self, request: Recorded) -> None:
        result = request.tool_results[-1]
        self.assertTrue(REFUSAL in result, f"the read of .slop-haus/credentials.json was not refused; the tool result was: {excerpt(redacted(result), 300)!r}")
        self.assertNotIn("access_token", result)

    def assert_same_session(self, played: Played) -> None:
        # After the host restarted: a new pi process, which found the session by its id.
        second = played.seen[3]
        for earlier in (FIRST_MESSAGE, REPLY, STATUS):
            self.assertIn(earlier, second.message_text, f"the second message's request did not carry the earlier turn ({earlier!r})")
        self.assertIn(SECOND_MESSAGE, second.message_text)
        files = sorted(self.agent_dir.glob(f"sessions/**/*{played.conversation_id}*.jsonl"))
        self.assertEqual(len(files), 1, f"expected one session file for conversation {played.conversation_id}, found {files}")

    def assert_usage(self, played: Played, before: dict[str, int]) -> None:
        """The posted delta is the scripted counts of this conversation's turns only: the turns after
        the restart did not count the session's earlier ones again."""
        spent = USAGE[:4]
        want = (
            before["input_tokens"] + sum(u.input_tokens for u in spent),
            before["output_tokens"] + sum(u.output_tokens for u in spent),
            before["requests"] + 4,
        )
        row = wait_for(
            "the usage of both turns to reach the board",
            lambda: next((a for a in [self.usage_row(played.workspace.name)] if a and a["input_tokens"] >= want[0]), None),
            diagnose=lambda: f"board usage: {self.board.llm_usage()['agents']}",
        )
        got = (row["input_tokens"], row["output_tokens"], row["requests"])
        self.assertTrue(got == want, f"usage was double counted or lost across the host's restart: the board has (input, output, requests) {got}, the script's turns come to {want}")
        self.assertEqual(row["billing_mode"], "metered", "an API-key provider should be metered")
        self.assertTrue(
            any(m["provider"] == PROVIDER and m["model"] == MODEL for m in self.board.llm_usage()["models"]),
            f"no usage was recorded for provider {PROVIDER!r} and model {MODEL!r}: {self.board.llm_usage()['models']}",
        )

    def check_requests(self, played: Played, expected_tools: set[str]) -> None:
        self.assert_the_model_was_offered_only_the_allowlist(played.seen, expected_tools)
        self.assert_thinking_reached_the_model(played.seen)
        self.assert_nothing_of_the_owner_loaded(played.seen)
        self.assert_no_extension_loaded(EXTENSIONS)
        self.assert_the_repos_pi_stayed_out(played.seen)
        self.assert_credentials_read_refused(played.seen[2])
        self.assert_same_session(played)
        self.assert_flags(played.conversation_id)

    def isolation(self, sabotage: Sabotage | None = None) -> list[str]:
        """A provider configured only in the owner's own ~/.pi/agent is not the
        remote control's: the model is never asked. Returns what the conversation says went wrong."""
        workspace = self.register()
        # The remote control's directory has no provider and run.env no key.
        self.prepare(workspace, logged_in=False, provider=False)
        self.adapter.prepare(self.tmp / "home", workspace.root, self.stack.model_server)
        if sabotage:
            sabotage(workspace)
        self.model.load(self.scenario(workspace))
        run = self.start_host(workspace)
        conversation = self.board.start_conversation(workspace.agent_id, FIRST_MESSAGE)

        def says() -> list[str]:
            return [e["body"] for e in self.events(workspace.agent_id, conversation["id"]) if e["kind"] in ("assistant", "failed")]

        def over() -> bool:
            kinds = {e["kind"] for e in self.events(workspace.agent_id, conversation["id"])}
            return bool(kinds & {"failed", "turn_end"}) or len(self.model.conversation()) > 0

        wait_for(
            "the conversation to end or the model to be asked",
            over,
            diagnose=lambda: f"events: {self.events(workspace.agent_id, conversation['id'])}\nhost output:\n{excerpt(run.output())}",
        )
        self.assertEqual(
            len(self.model.conversation()), 0,
            "a provider in the owner's own ~/.pi/agent reached the remote control's session: the model was asked (PI_CODING_AGENT_DIR is not the remote control's own)",
        )
        return says()

    # -- tests --

    def round_trip(self, sabotage: Sabotage | None = None) -> tuple[Played, set[str]]:
        # Before any sabotage: what the allowlist is.
        self.register()
        expected = self.host_tools()
        return self.play(sabotage), expected

    def test_a_conversation_round_trip_a_restart_and_a_stop(self) -> None:
        before = self.usage_row(self.workspace.name) or {"input_tokens": 0, "output_tokens": 0, "requests": 0}
        played, expected = self.round_trip()
        self.check_requests(played, expected)
        self.assert_usage(played, before)
        self.stop(played)

    def test_a_provider_only_in_the_owners_own_pi_directory_does_not_reach_the_session(self) -> None:
        said = self.isolation()
        print(f"WHAT THE CONVERSATION SAYS: {said}")
        self.assertTrue(any(PROVIDER in text for text in said), f"the failure does not name the provider {PROVIDER!r}: {said}")
        # Why it is unknown: the session's pi has its own agent directory, not the owner's.
        agent_dir = str(self.agent_dir)
        self.assertTrue(any(agent_dir in text and "models.json" in text and "auth.json" in text for text in said), f"the failure does not say whose pi directory it is: {said}")
        self.assertFalse(any("No project session found" in text for text in said), f"pi's first-start notice is shown as if it were the cause: {said}")

    # -- the suite's own tests: each breaks things in the host and expects the assertions on them to fail --

    def test_a_model_changed_on_the_board_answers_the_next_message(self) -> None:
        self.config_change()

    def test_negative_the_host_keeps_the_harness_it_started(self) -> None:
        # The host no longer takes up a changed config between turns.
        self.expect_failure(lambda: self.config_change(sabotage=lambda w: edit_host(w, "                self.take_up_config(conv)\n", "")), "the second message ran on the model the session started with")

    def test_negative_a_tool_dropped_from_the_allowlist(self) -> None:
        played, expected = self.round_trip(sabotage=lambda w: edit_host(w, '"ticket_edit", "ticket_create",', '"ticket_edit",'))
        self.expect_failure(lambda: self.assert_the_model_was_offered_only_the_allowlist(played.seen, expected), "missing ['ticket_create']")

    def test_negative_what_reaches_the_session(self) -> None:
        """What the host does to keep the owner's and the repo's pi setup out, undone together.
        Each assertion has to fail on its own: the played conversation is the same."""
        undo = [
            ('"--no-extensions", "-e"', '"-e"'),
            ('"--no-approve",\n', ""),
        ]
        played, _ = self.round_trip(sabotage=lambda w: [edit_host(w, old, new) for old, new in undo])
        # Each planted extension can load when nothing keeps it out, or the round trip's "none loaded" proves nothing.
        loaded = sorted(n for n in EXTENSIONS if (self.markers / n).exists())
        self.assertEqual(loaded, ["agent_dir", "project", "settings"], "a planted extension did not load even with the host's flags removed")
        self.expect_failure(lambda: self.assert_no_extension_loaded(("agent_dir",)), "extensions that --no-extensions should keep out were loaded")
        self.expect_failure(lambda: self.assert_the_repos_pi_stayed_out(played.seen), "the repo's .pi")

    def test_negative_pi_uses_the_owners_agent_directory(self) -> None:
        self.expect_failure(
            lambda: self.isolation(sabotage=lambda w: edit_host(w, 'env["PI_CODING_AGENT_DIR"] = str(self.config_dir(self._root))', "pass")), "the model was asked"
        )

    def test_negative_the_flags_do_not_reach_pi(self) -> None:
        played, _ = self.round_trip(sabotage=lambda w: edit_host(w, ', ("--thinking", variant.thinking)', ""))
        self.expect_failure(lambda: self.assert_thinking_reached_the_model(played.seen), "the thinking setting did not reach")
        self.expect_failure(lambda: self.assert_flags(played.conversation_id), "--thinking")

    def test_negative_the_credential_guard_is_off(self) -> None:
        played, _ = self.round_trip(
            sabotage=lambda w: edit_installed(
                w, "pi/slop-haus.ts", "if (!hostSession() || !GUARDED_TOOLS.has(event.toolName)) return undefined;", "return undefined;"
            )
        )
        self.expect_failure(lambda: self.assert_credentials_read_refused(played.seen[2]), "was not refused")

    def test_negative_the_usage_baseline_is_not_kept_across_a_restart(self) -> None:
        before = self.usage_row(self.workspace.name) or {"input_tokens": 0, "output_tokens": 0, "requests": 0}
        played, _ = self.round_trip(sabotage=lambda w: edit_host(w, "if k in known}", 'if k in known and k != "pi_stats"}'))
        self.expect_failure(lambda: self.assert_usage(played, before), "double counted")

    def test_negative_the_host_leaves_pi_running(self) -> None:
        def sabotage(workspace: Workspace) -> None:
            self.pi_ignores_eof = True
            edit_host(workspace, "    def terminate(self, grace: float) -> None:\n", "    def terminate(self, grace: float) -> None:\n        return\n")

        # The host's first stop is the restart between the messages.
        self.expect_failure(lambda: self.round_trip(sabotage=sabotage), "an orphaned pi process")

    def test_negative_the_host_leaves_a_backgrounded_command_running(self) -> None:
        # pi leaves when its stdin closes, so only the command it backgrounded is left. The host's
        # first stop is the restart between the messages.
        self.expect_failure(
            lambda: self.round_trip(sabotage=lambda w: [edit_host(w, old, new) for old, new in self.LEAVES_BACKGROUNDED]),
            "the command the model backgrounded to be gone",
        )

if __name__ == "__main__":
    unittest.main()
