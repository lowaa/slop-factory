"""What pi's extension does inside a real pi, that its own test (which
stands in for pi's extension API) cannot show: which tools pi is left with,
and what its guard on the workspace's credentials does to a real tool call.

Both are started the way the Remote Control host starts pi, not through
a launcher: a launcher clears every SLOP_HAUS_* variable, and SLOP_HAUS_HOST_SESSION is
what turns the guard on. Each is a single scripted exchange, not a worker."""

from __future__ import annotations

import os
import unittest
import uuid

from scripted_model.scenario import Scenario, ToolCall, Turn
from harnesses.pi import PROVIDER
from support.harness_case import MODEL, HarnessCase, excerpt
from support.harness_run import HarnessRun
from support.model_client import Recorded
from support.waiting import wait_for
from support.workspace import set_up

HOST_SESSION = {"SLOP_HAUS_HOST_SESSION": "1"}


@unittest.skipUnless(
    os.environ.get("HARNESS") == "pi", "these are about pi's extension API"
)
class PiExtension(HarnessCase):
    def host_session(self, *pi_args: str) -> HarnessRun:
        workspace = set_up(self.board, f"pi-extension-{uuid.uuid4().hex[:8]}", self.tmp / "checkout")
        command = [
            "pi",
            # As the host passes them: nothing of the owner's, nothing from the repo's .pi.
            "--no-extensions", "-e", str(workspace.root / ".slop-haus" / "pi" / "slop-haus.ts"),
            "--no-approve",
            "--provider", PROVIDER,
            "--model", MODEL,
            *pi_args,
            "start working",
        ]
        return self.start(workspace, command, extra_env=HOST_SESSION)

    def requests_after(self, run: HarnessRun, count: int) -> list[Recorded]:
        wait_for(
            f"{count} model request(s)",
            lambda: len(self.model.conversation()) >= count or run.exit_code() is not None,
            diagnose=lambda: f"model errors: {self.model.errors()}\nharness output:\n{excerpt(run.output())}",
        )
        return self.model.conversation()

    def test_tools_allowlist_drops_what_it_does_not_name_before_the_extension_registers_it(self) -> None:
        self.model.load(Scenario("tools", (Turn(text="ok"),)))
        run = self.host_session("--tools", "read,ticket_next")
        offered = set(self.requests_after(run, 1)[0].tool_names)
        self.assertEqual(
            offered,
            {"read", "ticket_next"},
            "--tools read,ticket_next should leave exactly those two, not the other built-ins or the extension's other tools",
        )

    def test_without_an_allowlist_it_has_the_built_ins_and_every_slop_haus_tool(self) -> None:
        self.model.load(Scenario("tools", (Turn(text="ok"),)))
        run = self.host_session()
        offered = set(self.requests_after(run, 1)[0].tool_names)
        for tool in ("bash", "read", "edit", "write", "ticket_next", "ticket_finish", "checkin"):
            self.assertIn(tool, offered)

    def test_the_credential_guard_refuses_file_tools_on_dot_slop_haus_and_only_those(self) -> None:
        self.model.load(
            Scenario(
                "guard",
                (
                    Turn(tool_calls=(ToolCall("read", {"path": ".slop-haus/server-url"}),)),
                    Turn(tool_calls=(ToolCall("read", {"path": "README.md"}),)),
                    Turn(text="done"),
                ),
            )
        )
        run = self.host_session()
        seen = self.requests_after(run, 3)
        self.assert_seen("credentials are not for the model", seen[1].tool_results[-1], "a read of .slop-haus/ was not refused")
        self.assertNotIn("http://", seen[1].tool_results[-1], "the refused read still returned the file")
        self.assert_seen("sample repo", seen[2].tool_results[-1], "the guard also refused a file outside .slop-haus/")
        self.assertEqual(self.model.errors(), [])


if __name__ == "__main__":
    unittest.main()
