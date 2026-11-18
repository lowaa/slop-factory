"""Claude Code's MCP connection after a 401 whose re-auth attempt fails.

Claude Code keeps the Authorization header `headersHelper` gave it. When the
access token expires the next MCP call is answered 401, Claude Code re-runs the
helper and reconnects -- once. If that attempt fails it logs "falling through
to needs-auth" and the slop-haus server stays unauthenticated until a person
runs `/mcp`, while the hooks (which call token.sh themselves) still work. Seen
on the workers three times in ten days, each during a deploy.

The real Claude Code, started by a real one-slot supervisor (the way a worker is
started now), the slot's `token.sh` and the API are used; a fault
proxy between them (support/fault_proxy.py) makes the failures happen on demand.
The scripted model's turns are the timing: a tool call, a gate at which the test
arms the proxy, another tool call, and a gate at which the test disarms it
before the call whose result decides. The armed 401 stands in for an expired
token, so nothing waits for 15 minutes.

The control case is a 401 alone, which Claude Code recovers from. A, B and C
add the failures seen on the workers; each asserts the healthy outcome (the call
after the failed re-auth works, and Claude Code did not give up). A and B are
marked expected failures, as nothing on our side can fix them; when a fix lands
the test reports an unexpected success and the marker comes off. C's came off
once token.sh retried the refresh. Whether the fault fired is a test of its own, which is
not an expected failure, so a run in which it never fired can't pass for the
known bug.

Found with Claude Code 2.1.292:
- A reconnect makes two POSTs (the second a retry of the first inside the same
  attempt), so one 502 or one 404 is recovered from and two in a row are not.
  Rollouts give bursts of 502s; that is the shape of A and B here.
- `claude -p` writes no `mcp-logs-*` (the interactive session does); `--debug`
  puts the same MCP messages in `$CLAUDE_CONFIG_DIR/debug/<session>.txt`. The supervisor
  does not pass it, so a `claude` wrapper first on its PATH adds it.

Each case is a fresh supervisor and slot, so a failed case's Claude Code stuck
in needs-auth, or a refresh token a fault used up, touches nothing after it.
A sign-in counts against the board's 20 per 10 minutes, and there are four cases."""

from __future__ import annotations

import json
import os
import shutil
import stat
import unittest
import uuid
from dataclasses import dataclass
from pathlib import Path
from typing import Callable, ClassVar

from scripted_model.scenario import Scenario, ToolCall, Turn, Usage
from support.fault_proxy import Event, Fault, FaultProxy
from support.harness_case import excerpt
from support.supervised_case import SupervisedCase
from support.supervisor_case import SupervisorRoot
from support.waiting import wait_for

ARM = "arm-the-fault"
DISARM = "faults-done"
HOLD = "hold-the-session"
USAGE = Usage(10, 1)
REAUTH_ATTEMPT = "returned 401; re-running headersHelper and retrying once"
GAVE_UP = "falling through to needs-auth"

@dataclass(frozen=True)
class Outcome:
    agent: str
    events: list[Event]
    faults_left: int
    results: list[str]
    debug_log: str
    output: str


@unittest.skipUnless(os.environ.get("HARNESS") == "claude", "headersHelper is Claude Code's; pi has no such thing")
class McpReauth(SupervisedCase):
    outcomes: ClassVar[dict[str, Outcome]] = {}
    # Set by a case whose recovery is a retried refresh getting through.
    refresh_expected = False

    # -- the slot --

    def credentials(self, sup: SupervisorRoot) -> Path:
        return sup.slot_dir / ".slop-haus" / "credentials.json"

    def point_slot_at(self, sup: SupervisorRoot, proxy: FaultProxy) -> None:
        """A test-only edit: the server-url file token.sh reads names the proxy, so its token refresh
        goes through it too. (The supervisor's own address, which .mcp.json is made from, was
        pointed at the proxy by `supervise`.)"""
        server = self.stack.server.rstrip("/")
        path = sup.slot_dir / ".slop-haus" / "server-url"
        self.assertIn(server, path.read_text(), "server-url does not name the server")
        path.write_text(proxy.url + "\n")

    def expire_access_token(self, sup: SupervisorRoot) -> None:
        creds = json.loads(self.credentials(sup).read_text())
        creds["expires_at"] = 1
        self.credentials(sup).write_text(json.dumps(creds))

    def debugging_claude(self) -> Path:
        """A directory with a `claude` that adds --debug, for the supervisor's PATH."""
        real = shutil.which("claude")
        assert real, "no claude on the PATH"
        bin_dir = self.tmp / "debug-bin"
        bin_dir.mkdir()
        wrapper = bin_dir / "claude"
        wrapper.write_text(f'#!/bin/sh\nexec {real} --debug "$@"\n')
        wrapper.chmod(wrapper.stat().st_mode | stat.S_IXUSR)
        return bin_dir

    # -- the session --

    def scenario(self) -> Scenario:
        checkin = ToolCall(self.adapter.mcp_tool("checkin"), {})
        return Scenario(
            "mcp reauth",
            (
                Turn(tool_calls=(checkin,), usage=USAGE),
                Turn(tool_calls=(checkin,), usage=USAGE, gate=ARM),
                Turn(tool_calls=(checkin,), usage=USAGE, gate=DISARM),
                Turn(text="Finished.", usage=USAGE, gate=HOLD),
            ),
        )

    def outcome(self, case: str, arm: Callable[[FaultProxy, SupervisorRoot], None]) -> Outcome:
        """Plays the case once, however many tests look at it: a case is a supervised run, and the
        faults it makes are the same for every assertion about it."""
        if case not in self.outcomes:
            self.outcomes[case] = self.play(arm)
        return self.outcomes[case]

    def play(self, arm: Callable[[FaultProxy, SupervisorRoot], None]) -> Outcome:
        """Plays the scenario, calling `arm` between the first call and the second."""
        proxy = FaultProxy(self.stack.server)
        self.addCleanup(proxy.close)
        model = self.model
        name = f"reauth-{uuid.uuid4().hex[:8]}"
        # A scope of its own: a run left holding its ticket must not keep another case from being claimed.
        ticket_id = self.board.create_ticket("Check in", "Check in, three times.", [f"e2e/{uuid.uuid4().hex[:8]}/"])
        self.addCleanup(lambda: self.board.request("PATCH", f"/api/tickets/{ticket_id}", {"status": "cancelled"}))
        sup = self.supervise(name, (f"ticket-{ticket_id}-a1",), server=proxy.url)
        self.point_slot_at(sup, proxy)
        model.load(self.scenario())
        path = f"{self.debugging_claude()}{os.pathsep}{os.environ['PATH']}"
        running = self.run_supervisor(sup, env={"PATH": path})
        diagnose = lambda: (  # noqa: E731
            f"proxy:\n{proxy.summary()}\nmodel errors: {model.errors()}\nsupervisor log:\n{excerpt(running.output(), 2000)}"
        )
        try:
            wait_for("the second call to be held at its gate", lambda: len(model.conversation()) == 2, diagnose=diagnose)
            arm(proxy, sup)
            model.open_gate(ARM)
            wait_for("the third call to be held at its gate", lambda: len(model.conversation()) == 3, diagnose=diagnose)
            # Whatever has not fired is not to hit the call that decides.
            faults_left = proxy.disarm()
            model.open_gate(DISARM)
            wait_for("the fourth request, with the third call's result", lambda: len(model.conversation()) == 4, diagnose=diagnose)
            self.assertEqual(model.errors(), [], "the harness asked for something the script did not expect")
            requests = model.conversation()
            events = proxy.events()
            if not self.refresh_expected:
                refreshed = [e for e in events if e.path == "/oauth/token" and not e.fault]
                self.assertEqual(refreshed, [], "a refresh reached the real server, which this scenario's faults are meant to stop")
            return Outcome(
                agent=name,
                events=events,
                faults_left=faults_left,
                results=requests[-1].tool_results,
                debug_log="\n".join(p.read_text() for p in sorted((sup.root / "harness" / "claude" / "debug").glob("*.txt"))),
                output=running.output(),
            )
        finally:
            running.close()

    # -- what the cases assert --

    def assert_fired(self, outcome: Outcome, expected: list[tuple[str, str, Fault]]) -> None:
        fired = [(e.method, e.path, e.fault) for e in outcome.events if e.fault]
        self.assertEqual(
            fired,
            expected,
            f"the faults did not fire as armed (fired: {fired}, armed but unfired: {outcome.faults_left}); "
            f"the case proves nothing\nproxy saw: {[(e.method, e.path, e.status) for e in outcome.events][-10:]}",
        )
        self.assertIn(REAUTH_ATTEMPT, outcome.debug_log, "Claude Code never re-ran headersHelper after the 401")

    def assert_recovered(self, outcome: Outcome) -> None:
        """The healthy outcome: the call after the 401 works and Claude Code did not give up."""
        self.assertNotIn(GAVE_UP, outcome.debug_log, "Claude Code gave up on the connection")
        self.assertIn(outcome.agent, "".join(outcome.results[-1:]), f"the call after the failed re-auth got {outcome.results[-1:]!r}")

    # -- the cases --

    def control(self) -> Outcome:
        return self.outcome("control", lambda p, sup: p.arm("POST", "/mcp", Fault.UNAUTHORIZED))

    def case_a(self) -> Outcome:
        # 10-07: the reconnect POST (and its retry) met the rollout's 502.
        return self.outcome("a", lambda p, sup: p.arm("POST", "/mcp", Fault.UNAUTHORIZED, Fault.BAD_GATEWAY, Fault.BAD_GATEWAY))

    def case_b(self) -> Outcome:
        # 10-04: the reconnect POST (and its retry) met a pod that did not know the session.
        return self.outcome("b", lambda p, sup: p.arm("POST", "/mcp", Fault.UNAUTHORIZED, Fault.SESSION_NOT_FOUND, Fault.SESSION_NOT_FOUND))

    def case_c(self) -> Outcome:
        # 10-08: the helper itself failed -- its token had expired, and the refresh did not get through.
        def arm(proxy: FaultProxy, sup: SupervisorRoot) -> None:
            self.refresh_expected = True
            self.expire_access_token(sup)
            proxy.arm("POST", "/mcp", Fault.UNAUTHORIZED)
            proxy.arm("POST", "/oauth/token", Fault.BAD_GATEWAY)

        return self.outcome("c", arm)

    A_FIRED = [("POST", "/mcp", Fault.UNAUTHORIZED), ("POST", "/mcp", Fault.BAD_GATEWAY), ("POST", "/mcp", Fault.BAD_GATEWAY)]
    B_FIRED = [("POST", "/mcp", Fault.UNAUTHORIZED), ("POST", "/mcp", Fault.SESSION_NOT_FOUND), ("POST", "/mcp", Fault.SESSION_NOT_FOUND)]
    C_FIRED = [("POST", "/mcp", Fault.UNAUTHORIZED), ("POST", "/oauth/token", Fault.BAD_GATEWAY)]

    def test_control_a_401_alone_is_recovered_from(self) -> None:
        outcome = self.control()
        self.assert_fired(outcome, [("POST", "/mcp", Fault.UNAUTHORIZED)])
        self.assert_recovered(outcome)
        self.assertEqual(len(outcome.results), 3, outcome.results)

    def test_a_502_on_the_reconnect_fires(self) -> None:
        self.assert_fired(self.case_a(), self.A_FIRED)

    # Expected to fail: Claude Code gives up after one failed
    # re-auth attempt, and that is not ours to fix.
    @unittest.expectedFailure
    def test_a_502_on_the_reconnect_is_recovered_from(self) -> None:
        self.assert_recovered(self.case_a())

    def test_b_session_not_found_on_the_reconnect_fires(self) -> None:
        self.assert_fired(self.case_b(), self.B_FIRED)

    # Expected to fail: Claude Code's behaviour, as for A.
    @unittest.expectedFailure
    def test_b_session_not_found_on_the_reconnect_is_recovered_from(self) -> None:
        self.assert_recovered(self.case_b())

    def test_c_a_failed_token_refresh_fires(self) -> None:
        self.assert_fired(self.case_c(), self.C_FIRED)

    def test_c_a_failed_token_refresh_is_recovered_from(self) -> None:
        self.assert_recovered(self.case_c())


if __name__ == "__main__":
    unittest.main()
