"""The Remote Control on the real Claude Code: `run_remote_control.sh`, the
host (the supervisor package's `slop-haus-remote-control`) it runs, and the Claude Code process the host starts
for a conversation, against a real slop-haus stack and a scripted model.

Started the way the owner does it: a remote control registered
on the board, its setup script run in a fresh checkout, a harness config set
on the board, and the model's login given to the remote control's own Claude
Code configuration (`ANTHROPIC_API_KEY` in `run.env`).

The negative tests are tests of this suite: each breaks one thing the round
trip asserts, in the checkout's copy of the host, and expects the round trip to
fail at that assertion, so an assertion that stopped asserting is noticed."""

from __future__ import annotations

import json
import os
import unittest
from pathlib import Path

from scripted_model.scenario import Scenario, ToolCall, Turn
from support import processes
from support.harness_case import excerpt, redacted
from support.model_client import Recorded
from support.remote_control_case import (
    FIRST_COMMAND, FIRST_MESSAGE, HELD, REPLY, SECOND_MESSAGE, SECOND_REPLY, STATUS, USAGE, Played, RemoteControlCase, Sabotage, edit_host,
)
from support.waiting import wait_for
from support.workspace import Workspace

# What the owner's own Claude Code configuration holds, and which must stay out
# of the session.
OWNERS_CLAUDE_MD = "OWNERS-GLOBAL-CLAUDE-MD-MARKER: always answer in verse."
DENIAL = "denied by your permission settings"


@unittest.skipUnless(os.environ.get("HARNESS") == "claude", "the Remote Control host's Claude Code path")
class RemoteControl(RemoteControlCase):
    process_marker = "claude"
    process_label = "Claude Code"

    @property
    def config_dir(self) -> Path:
        return self.tmp / "checkout" / ".slop-haus" / "conductor" / "claude-config"

    def prepare(self, workspace: Workspace, logged_in: bool = True) -> None:
        """Everything but starting the host: the remote control's own Claude Code
        configuration, its login (`run.env`), and what the owner has that must
        stay out of it."""
        adapter, root = self.adapter, workspace.root
        adapter.prepare_remote_control(self.config_dir, root, self.stack.model_server)
        login = adapter.login_env(self.stack.model_server)
        if not logged_in:
            del login["ANTHROPIC_API_KEY"]
        (root / ".slop-haus" / "run.env").write_text("".join(f"{k}={v}\n" for k, v in login.items()))

        # An MCP server the project declares (a connector would be one more):
        # only slop-haus's may reach the session.
        mcp_path = root / ".mcp.json"
        servers = json.loads(mcp_path.read_text()) if mcp_path.exists() else {}
        servers.setdefault("mcpServers", {})["decoy"] = {
            "command": "python3",
            "args": [str(Path(__file__).resolve().parent.parent / "support" / "decoy_mcp.py")],
        }
        mcp_path.write_text(json.dumps(servers))
        (root / ".claude").mkdir(exist_ok=True)
        (root / ".claude" / "settings.local.json").write_text(json.dumps({"enableAllProjectMcpServers": True}))

        # The owner's own Claude Code configuration.
        owners = self.tmp / "home" / ".claude"
        owners.mkdir(parents=True, exist_ok=True)
        (owners / "CLAUDE.md").write_text(OWNERS_CLAUDE_MD)

    def running(self) -> list[tuple[int, str]]:
        """The processes of the remote control's Claude Code: it and what it ran."""
        return processes.with_environment("CLAUDE_CONFIG_DIR", str(self.config_dir))

    # -- scenario --

    def scenario(self, workspace: Workspace) -> Scenario:
        a = self.adapter
        credentials = workspace.root / ".slop-haus" / "credentials.json"
        return Scenario(
            "remote control round trip",
            (
                Turn(tool_calls=(ToolCall(a.bash_tool, {"command": FIRST_COMMAND, "description": "git status"}),), usage=USAGE[0]),
                Turn(tool_calls=(ToolCall("Read", {"file_path": str(credentials)}),), usage=USAGE[1]),
                Turn(text=REPLY, usage=USAGE[2]),
                Turn(text=SECOND_REPLY, usage=USAGE[3]),
                Turn(text="never sent", usage=USAGE[4], gate=HELD),
            ),
        )

    def check_first_request(self, request: Recorded) -> None:
        # At once: with auto-memory on, a session makes requests the script has no turns for.
        self.assert_auto_memory_off(request)

    def check_requests(self, played: Played) -> None:
        self.assert_what_the_model_was_offered(played.seen)
        self.assert_the_owners_configuration_stayed_out(played.seen)
        self.assert_credentials_read_denied(played.seen[2])
        self.assert_same_session(played.seen, played.conversation_id)

    def isolation(self, sabotage: Sabotage | None = None) -> None:
        """A login that exists only in the owner's own Claude Code directory is not
        the remote control's: its session is not logged in, and the model is never asked."""
        workspace = self.register()
        self.prepare(workspace, logged_in=False)
        owners = self.tmp / "home" / ".claude"
        (owners / "settings.json").write_text(json.dumps({"apiKeyHelper": f"echo {self.adapter.login_env('')['ANTHROPIC_API_KEY']}"}))
        if sabotage:
            sabotage(workspace)
        self.model.load(self.scenario(workspace))
        run = self.start_host(workspace)
        conversation = self.board.start_conversation(workspace.agent_id, FIRST_MESSAGE)
        # Either way it ends: the host reports the session's failure, or the model is asked.
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
            "the owner's own login reached the remote control's session: the model was asked (CLAUDE_CONFIG_DIR is not the remote control's own)",
        )

    # -- assertions on what the model was sent --

    def assert_what_the_model_was_offered(self, seen: list[Recorded]) -> None:
        for request in seen:
            others = sorted(t for t in request.tool_names if t.startswith("mcp__") and not t.startswith("mcp__slop-haus__"))
            self.assertEqual(others, [], f"tools of a server other than slop-haus's were offered (--strict-mcp-config?): {others}")
            self.assertIn(self.adapter.mcp_tool("ticket_next"), request.tool_names, "slop-haus's MCP tools were not offered")
            self.assertIn(self.adapter.bash_tool, request.tool_names)

    def assert_the_owners_configuration_stayed_out(self, seen: list[Recorded]) -> None:
        for request in seen:
            text = request.system + request.message_text
            # Not assertNotIn: its message would quote the whole system prompt.
            self.assertFalse(
                OWNERS_CLAUDE_MD in text, "the owner's own ~/.claude reached the session (CLAUDE_CONFIG_DIR is not the remote control's own)"
            )

    def assert_auto_memory_off(self, request: Recorded) -> None:
        # The section of the system prompt that tells the model where its memory lives.
        self.assertFalse("persistent file-based memory" in request.system, "auto-memory was on: the session was told to keep a memory")

    def assert_credentials_read_denied(self, request: Recorded) -> None:
        result = request.tool_results[-1]
        self.assertIn(DENIAL, result, f"Read of .slop-haus/credentials.json was not denied; the tool result was: {excerpt(redacted(result), 300)!r}")
        self.assertNotIn("access_token", result)

    def assert_same_session(self, seen: list[Recorded], conversation_id: str) -> None:
        second = seen[3]
        for earlier in (FIRST_MESSAGE, REPLY, STATUS):
            self.assertIn(earlier, second.message_text, f"the second message's request did not carry the earlier turn ({earlier!r})")
        self.assertIn(SECOND_MESSAGE, second.message_text)
        # Claude Code puts its session id in the request's metadata.
        sessions = [json.loads(r.raw["metadata"]["user_id"])["session_id"] for r in seen]
        self.assertEqual(sessions, [conversation_id] * len(seen), "the requests were not all of the conversation's one session")

    # -- tests --

    def test_a_conversation_round_trip_and_a_stop(self) -> None:
        played = self.play()
        self.check_requests(played)
        self.stop(played)

    def test_a_login_only_in_the_owners_own_claude_directory_does_not_reach_the_session(self) -> None:
        self.isolation()

    def test_a_model_changed_on_the_board_answers_the_next_message(self) -> None:
        self.config_change()

    # -- the suite's own tests: each breaks things in the host and expects the assertions on them to fail --
    def test_negative_the_host_keeps_the_harness_it_started(self) -> None:
        # The host no longer takes up a changed config between turns.
        self.expect_failure(lambda: self.config_change(sabotage=lambda w: edit_host(w, "                self.take_up_config(conv)\n", "")), "the second message ran on the model the session started with")


    def test_negative_what_reaches_the_session(self) -> None:
        """Three things the host does that keep the owner's setup out, undone together.
        Each assertion has to fail on its own: the played conversation is the same."""
        undo = [
            # The host stops giving its Claude Code a directory of its own, so it reads the owner's.
            ('env["CLAUDE_CONFIG_DIR"] = str(self.config_dir(self._root))', "pass"),
            ('            "--strict-mcp-config",\n', ""),
            ('"--disallowedTools", ",".join(denied),', ""),
        ]
        played = self.play(sabotage=lambda w: [edit_host(w, old, new) for old, new in undo])
        for assertion, failure in [
            (lambda: self.assert_the_owners_configuration_stayed_out(played.seen), "the owner's own ~/.claude reached the session"),
            (lambda: self.assert_what_the_model_was_offered(played.seen), "tools of a server other than slop-haus's were offered"),
            (lambda: self.assert_credentials_read_denied(played.seen[2]), "Read of .slop-haus/credentials.json was not denied"),
        ]:
            self.expect_failure(assertion, failure)

    def test_negative_auto_memory_is_on(self) -> None:
        self.expect_failure(
            lambda: self.play(sabotage=lambda w: edit_host(w, 'env["CLAUDE_CODE_DISABLE_AUTO_MEMORY"] = "1"', "pass")), "auto-memory was on"
        )

    def test_negative_a_login_in_the_owners_claude_directory_reaches_the_session(self) -> None:
        self.expect_failure(
            lambda: self.isolation(sabotage=lambda w: edit_host(w, 'env["CLAUDE_CONFIG_DIR"] = str(self.config_dir(self._root))', "pass")), "the model was asked"
        )

    def test_negative_the_host_leaves_the_harness_running(self) -> None:
        # The host does not end a harness: it exits and leaves it.
        played = self.play(
            sabotage=lambda w: edit_host(w, "    def terminate(self, grace: float) -> None:\n", "    def terminate(self, grace: float) -> None:\n        return\n")
        )
        self.expect_failure(lambda: self.stop(played), "an orphaned Claude Code process")

    def test_negative_the_host_leaves_a_backgrounded_command_running(self) -> None:
        played = self.play(sabotage=lambda w: [edit_host(w, old, new) for old, new in self.LEAVES_BACKGROUNDED])
        self.expect_failure(lambda: self.stop(played), "the command the model backgrounded to be gone")

if __name__ == "__main__":
    unittest.main()
