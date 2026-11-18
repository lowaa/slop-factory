from __future__ import annotations

import json
import logging
from collections.abc import Callable
import sys
from pathlib import Path

import pytest

sys.path.insert(0, str(Path(__file__).parent.parent / "slophaus"))

from fakes import FakeRunner
from slop_haus_supervisor.ports import ProcessResult
from slop_haus_supervisor.slophaus.supervisor_api import SupervisorApi
from slop_haus_supervisor.slophaus.transport import ApiError, Transport
from slop_haus_supervisor.steward.events import WakeEvent, WakeKind
from slop_haus_supervisor.steward.enrollment import StewardEnrollment
from slop_haus_supervisor.workspace.provisioned import Kind, Provisioned
from slop_haus_supervisor.steward.server import ServerSteward
from stub_server import StubServer, serving

BASE = "/api/supervisor/steward"


@pytest.fixture
def stub():
    s = StubServer()
    with serving(s):
        yield s


def api_for(stub: StubServer) -> SupervisorApi:
    return SupervisorApi(Transport(stub.url), lambda: "sup-token")


def steward_unit(stub: StubServer, directory: Path, runner: FakeRunner) -> Provisioned:
    return Provisioned(directory, Kind.STEWARD, runner, logging.getLogger("test"), StewardEnrollment(api_for(stub), stub.url))


def test_events_start_from_now_then_follow_the_cursor(stub: StubServer) -> None:
    stub.on("GET", f"{BASE}/events", (200, {"events": [], "cursor": 5}))
    stub.on(
        "GET", f"{BASE}/events?after=5",
        (200, {"events": [{"kind": "discussion", "ticket_id": 7}, {"kind": "agent_filed", "ticket_id": 9}, {"kind": "future", "ticket_id": 1}], "cursor": 8}),
    )
    stub.on("GET", f"{BASE}/events?after=8", (200, {"events": [], "cursor": 8}))
    board = ServerSteward(api_for(stub))

    assert board.new_events() == []
    assert board.new_events() == [WakeEvent(WakeKind.DISCUSSION, 7), WakeEvent(WakeKind.AGENT_FILED, 9)]
    assert board.new_events() == []
    assert stub.requests("GET", f"{BASE}/events")[0].auth == "Bearer sup-token"


def test_briefing_and_agreement_are_the_servers_text(stub: StubServer) -> None:
    stub.on("GET", f"{BASE}/briefing", (200, {"text": "be the steward"}))
    stub.on("GET", f"{BASE}/agreement", (200, {"text": "restart_resume: 3/3"}))
    steward = ServerSteward(api_for(stub))

    assert steward.load() == "be the steward"
    assert steward.agreement_report() == "restart_resume: 3/3"


def test_a_briefing_the_server_cannot_give_raises(stub: StubServer) -> None:
    stub.on("GET", f"{BASE}/briefing", (503, {"detail": "the steward is not available"}))

    with pytest.raises(ApiError):
        ServerSteward(api_for(stub)).load()


def test_lease_acquire_reports_whether_it_is_held_and_release_calls_delete(stub: StubServer) -> None:
    stub.on("POST", f"{BASE}/lease", (200, {"acquired": False, "expires_at": None}), (200, {"acquired": True, "expires_at": "x"}))
    stub.on("DELETE", f"{BASE}/lease", (200, ""))
    steward = ServerSteward(api_for(stub))

    assert steward.acquire() is False
    assert steward.acquire() is True
    steward.release()
    assert len(stub.requests("DELETE", f"{BASE}/lease")) == 1


def test_the_mode_is_read_from_the_server_and_an_unrecognisable_answer_is_an_error(stub: StubServer) -> None:
    stub.on("GET", f"{BASE}/mode", (200, {"mode": "shadow"}), (200, {}), (200, {"mode": "bogus"}))
    steward = ServerSteward(api_for(stub))

    assert steward.mode() == "shadow"
    for _ in range(2):
        with pytest.raises(ApiError):
            steward.mode()


def installs_token_script(token_script: Path, code: int = 0) -> Callable[[list[str]], ProcessResult]:
    def answer(argv: list[str]) -> ProcessResult:
        if code == 0:
            token_script.write_text("#!/bin/sh\n")
        return ProcessResult(code, "", "bad")

    return answer


def test_sign_in_issues_the_credential_once_and_installs_the_token_helper(stub: StubServer, tmp_path: Path) -> None:
    stub.on("PUT", BASE, (200, {"id": "s1", "name": "steward"}))
    stub.on("POST", f"{BASE}/credential", (200, {"access_token": "a", "refresh_token": "r", "expires_in": 900}))
    stub.on("GET", "/api/workspace/update_script", (200, "echo update"))
    directory = tmp_path / "steward"
    runner = FakeRunner(installs_token_script(directory / ".slop-haus" / "token.sh"))
    sign_in = steward_unit(stub, directory, runner)

    script = sign_in.ensure()
    again = sign_in.ensure()

    assert script == again == directory / ".slop-haus" / "token.sh"
    assert json.loads((directory / ".slop-haus" / "credentials.json").read_text())["refresh_token"] == "r"
    assert len(runner.calls) == 2, "the install, then only the helper's own check on the second ensure"
    assert runner.calls[1] == [str(script)]
    assert len(stub.requests("POST", f"{BASE}/credential")) == 1
    assert (directory / ".slop-haus" / "no-clone").is_file()
    assert stub.requests("GET", "/api/workspace/update_script")[0].auth == "Bearer a"


def test_sign_in_fails_loudly_when_the_helper_does_not_install(stub: StubServer, tmp_path: Path) -> None:
    stub.on("PUT", BASE, (200, {"id": "s1", "name": "steward"}))
    stub.on("POST", f"{BASE}/credential", (200, {"access_token": "a", "refresh_token": "r", "expires_in": 900}))
    stub.on("GET", "/api/workspace/update_script", (200, "exit 1"))
    directory = tmp_path / "steward"

    with pytest.raises(Exception, match="installing the workspace files failed"):
        steward_unit(stub, directory, FakeRunner(installs_token_script(directory / ".slop-haus" / "token.sh", code=1))).ensure()


def test_sign_in_fails_loudly_when_the_update_succeeds_without_a_helper(stub: StubServer, tmp_path: Path) -> None:
    stub.on("PUT", BASE, (200, {"id": "s1", "name": "steward"}))
    stub.on("POST", f"{BASE}/credential", (200, {"access_token": "a", "refresh_token": "r", "expires_in": 900}))
    stub.on("GET", "/api/workspace/update_script", (200, "true"))

    with pytest.raises(Exception, match="did not leave a token helper"):
        steward_unit(stub, tmp_path / "steward", FakeRunner(lambda argv: ProcessResult(0, "", ""))).ensure()
