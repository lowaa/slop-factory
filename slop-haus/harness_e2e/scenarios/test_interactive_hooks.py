"""The workspace's hooks against the real Claude Code, in the one way a person runs it: an interactive
session on a terminal, with SLOP_HAUS_HOST_SESSION unset, in a workspace `slop setup-workspace` made.
Every other Claude Code scenario is a supervisor's run or a Remote Control's, which set that variable
and so turn the hooks off.

What the hook protocol depends on is Claude Code's behaviour (a Stop hook's exit 2, a SessionStart
hook's `hookSpecificOutput`), which is what a Claude Code release can break. Asserts that:
  - SessionStart binds the session and its briefing reaches the model;
  - the Stop hook sends the agent back to work while it holds a ticket, with the server's reason;
  - the usage hook posts what the session spent.

It also records the payloads Claude Code sent each hook, as the fixtures the Go, bats and Playwright
tests of the hooks read (`test.sh --capture-hook-payloads`; support/hook_fixtures.py).

Opt-in (HARNESS_E2E_INTERACTIVE_HOOKS=1, which test.sh sets for the @latest run): it runs only in the
non-gating @latest job until it has been green for long enough to gate."""

from __future__ import annotations

import json
import os
import unittest
import uuid

from scripted_model.scenario import Scenario, ToolCall, Turn, Usage
from support import hook_fixtures
from support.harness_case import HarnessCase, excerpt
from support.harness_run import HarnessRun
from support.waiting import Timeout, wait_for
from support.workspace import Workspace, set_up

USAGE = [Usage(100, 10), Usage(120, 12), Usage(130, 14), Usage(140, 16)]
EARLY_STOP = "I believe I am finished."
# The Stop hook's answer while a ticket is held (hookserver's reasonForHeldTicket).
HELD_REASON = "You still hold ticket #"
PROMPT = "start working"
EVENTS = ("SessionStart", "UserPromptSubmit", "Stop")


@unittest.skipUnless(os.environ.get("HARNESS") == "claude", "the Claude Code hook protocol")
@unittest.skipUnless(os.environ.get("HARNESS_E2E_INTERACTIVE_HOOKS") == "1", "opt-in: runs in the @latest job (see the module docstring)")
class InteractiveHooks(HarnessCase):
    def set_up_workspace(self) -> tuple[Workspace, str]:
        name = f"interactive-{uuid.uuid4().hex[:8]}"
        workspace = set_up(self.board, name, self.tmp / "checkout")
        return workspace, name

    def record_payloads(self, workspace: Workspace) -> None:
        """A hook of the test's own beside the workspace's, which keeps what Claude Code sends it."""
        self.payloads = self.logs / "hook_payloads"
        self.payloads.mkdir(exist_ok=True)
        settings_path = workspace.root / ".claude" / "settings.local.json"
        settings = json.loads(settings_path.read_text()) if settings_path.exists() else {}
        settings["enableAllProjectMcpServers"] = True
        settings["hooks"] = {
            event: [{"hooks": [{"type": "command", "command": f"{{ cat; echo; }} >> {self.payloads / (event + '.jsonl')}"}]}]
            for event in EVENTS
        }
        settings_path.write_text(json.dumps(settings))

    def start_claude(self, workspace: Workspace) -> HarnessRun:
        home = self.tmp / "home"
        home.mkdir()
        self.adapter.prepare(home, workspace.root, self.stack.model_server)
        # The dialog an interactive session shows for an API key from the environment.
        state = json.loads((home / ".claude.json").read_text())
        key = self.adapter.login_env(self.stack.model_server)["ANTHROPIC_API_KEY"]
        state["customApiKeyResponses"] = {"approved": [key[-20:]], "rejected": []}
        (home / ".claude.json").write_text(json.dumps(state))
        # SLOP_HAUS_HOST_SESSION is not in the environment HarnessRun gives a harness.
        run = self.start(
            workspace, ["claude", "--debug-file", str(self.logs / "claude_debug.log"), "--model", "scripted-model-1"], prepare_home=False, tty=True,
            # Otherwise slop-haus's tools are deferred behind ToolSearch, which the script does not play.
            extra_env={"ENABLE_TOOL_SEARCH": "false"},
        )
        # Interactive Claude Code connects MCP servers in the background and does not offer a
        # server's tools to a request made before it has: a person has typed their prompt well
        # after, and a prompt on the command line is not.
        debug_log = self.logs / "claude_debug.log"
        wait_for(
            "claude to connect to slop-haus's MCP server",
            lambda: debug_log.exists() and 'MCP server "slop-haus": Successfully connected' in debug_log.read_text(errors="replace"),
            diagnose=lambda: f"claude output:\n{excerpt(run.output(), 1500)}",
        )
        run.type(PROMPT)
        return run

    def scenario(self) -> Scenario:
        a = self.adapter
        return Scenario(
            "interactive hooks",
            (
                Turn(tool_calls=(ToolCall(a.mcp_tool("ticket_next"), {}),), usage=USAGE[0]),
                # Stops with the ticket held: the Stop hook must send it back.
                Turn(text=EARLY_STOP, usage=USAGE[1]),
                Turn(
                    tool_calls=(
                        ToolCall(
                            a.mcp_tool("ticket_finish"),
                            {"handoff": "nothing to change", "pr_url": "https://github.example/acme/repo/pull/1", "request_review": True, "reason": "the scripted worker hands its change over"},
                        ),
                    ),
                    usage=USAGE[2],
                ),
                Turn(text="All done.", usage=USAGE[3]),
            ),
        )

    def play(self, break_stop_hook: bool = False) -> tuple[str, int, HarnessRun]:
        board, model = self.board, self.model
        ticket_id = board.create_ticket("Look at the uploader", "Nothing to do", [f"e2e/{uuid.uuid4().hex[:8]}/"])
        self.addCleanup(lambda: board.request("PATCH", f"/api/tickets/{ticket_id}", {"status": "cancelled"}))
        workspace, name = self.set_up_workspace()
        self.workspace = workspace
        self.record_payloads(workspace)
        if break_stop_hook:
            # Exit 0 where it exits 2: the agent may stop.
            script = workspace.root / ".slop-haus" / "hook-stop.sh"
            text = script.read_text()
            assert "\nexit 2\n" in text, "hook-stop.sh no longer ends in exit 2: update this negative test"
            script.write_text(text.replace("\nexit 2\n", "\nexit 0\n"))
        model.load(self.scenario())
        run = self.start_claude(workspace)
        wait_for(
            "the last scripted turn to be played",
            lambda: model.remaining() == 0,
            diagnose=lambda: (
                f"{len(model.conversation())} model request(s), {model.remaining()} scripted turn(s) unplayed; "
                f"model errors: {model.errors()}\nclaude output:\n{excerpt(run.output(), 2000)}"
            ),
        )
        return name, ticket_id, run

    def test_session_start_stop_and_usage_hooks(self) -> None:
        name, ticket_id, run = self.play()
        board, model = self.board, self.model
        self.assertEqual(model.errors(), [], f"claude asked for something the script did not expect:\n{excerpt(run.output())}")
        seen = model.conversation()
        self.assertEqual(len(seen), len(USAGE))

        # SessionStart: the briefing it prints reaches the model, and the session is bound.
        self.assert_seen(f'You are agent "{name}" in slop-haus', seen[0].system + seen[0].message_text, "the SessionStart briefing did not reach the model")
        wait_for(
            "the session to be bound",
            lambda: board.agent(name)["session"] is not None,
            diagnose=lambda: f"agent: {board.agent(name)}",
        )

        # Stop: held ticket, so exit 2, and the reason is what the model is told next.
        self.assert_seen(HELD_REASON + str(ticket_id), seen[2].message_text, "the Stop hook did not send the agent back to work")
        self.assertNotIn(HELD_REASON, seen[1].message_text)
        self.assert_seen(EARLY_STOP, seen[2].message_text, "the Stop hook's block started a new conversation")

        # Usage: posted by the async hook at each Stop.
        wait_for(
            "the usage hook's report to reach the board",
            lambda: next((a for a in board.llm_usage()["agents"] if a["name"] == name and a["requests"] >= len(USAGE)), None),
            diagnose=lambda: f"board usage: {board.llm_usage()['agents']}",
        )

        self.keep_fixtures()

    def keep_fixtures(self) -> None:
        """Every event's payloads were captured, from one session; their sanitized first ones are the fixtures."""
        by_event = {}
        for event in EVENTS:
            lines = [json.loads(line) for line in (self.payloads / f"{event}.jsonl").read_text().splitlines() if line.strip()]
            self.assertTrue(lines, f"no {event} payload was captured")
            by_event[event] = lines
        sessions = {p["session_id"] for lines in by_event.values() for p in lines}
        self.assertEqual(len(sessions), 1, f"the hooks did not all see one session: {sessions}")
        out = self.logs / "fixtures"
        clean = lambda p: hook_fixtures.sanitized(p, self.workspace.root)  # noqa: E731
        hook_fixtures.write(out, "SessionStart", clean(by_event["SessionStart"][0]))
        hook_fixtures.write(out, "UserPromptSubmit", clean(by_event["UserPromptSubmit"][0]))
        hook_fixtures.write(out, "Stop", clean(by_event["Stop"][0]))
        # The second Stop is the one after the hook blocked the first.
        self.assertGreaterEqual(len(by_event["Stop"]), 2, "the agent was not sent back to work and stopped again")
        hook_fixtures.write(out, "Stop_after_block", clean(by_event["Stop"][1]))

    def test_negative_a_stop_hook_that_exits_0_fails_at_the_stop_assertion(self) -> None:
        """The suite's own test: the agent is let stop with its ticket held, so the script's
        later turns are never asked for."""
        with self.assertRaises(Timeout) as failed:
            self.play(break_stop_hook=True)
        self.assertIn("the last scripted turn to be played", str(failed.exception))
        self.assertEqual(self.model.remaining(), 2, "the Stop hook should have let the agent stop after the early stop")


if __name__ == "__main__":
    unittest.main()
