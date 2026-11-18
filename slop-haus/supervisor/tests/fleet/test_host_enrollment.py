from __future__ import annotations

import logging
from collections.abc import Generator
from pathlib import Path

import pytest

from slop_haus_supervisor.fleet.host_enrollment import RemoteControlEnrollment
from slop_haus_supervisor.slophaus.supervisor_api import SupervisorApi
from slop_haus_supervisor.slophaus.transport import Transport
from slop_haus_supervisor.ports import SubprocessRunner
from slop_haus_supervisor.workspace.provisioned import Kind, Provisioned
from stub_server import StubServer, serving

BASE = "/api/supervisor/remote-control/rc-1"
ISSUED = (200, {"access_token": "issued-access", "refresh_token": "issued-refresh", "expires_in": 900})
UPDATE_SCRIPT = """#!/usr/bin/env bash
set -euo pipefail
mkdir -p .slop-haus
printf '#!/bin/sh\\n' > .slop-haus/token.sh
"""


@pytest.fixture
def stub() -> Generator[StubServer]:
    s = StubServer()
    s.on("PUT", BASE, (200, {"id": "h1", "name": "rc-1"}))
    s.on("GET", "/api/workspace/update_script", (200, UPDATE_SCRIPT))
    with serving(s):
        yield s


def host_unit(stub: StubServer, directory: Path) -> Provisioned:
    api = SupervisorApi(Transport(stub.url), lambda: "sup-token")
    return Provisioned(directory, Kind.REMOTE_CONTROL, SubprocessRunner(), logging.getLogger("test"), RemoteControlEnrollment(api, stub.url, "rc-1"))


def test_a_directory_with_no_credential_is_enrolled_through_the_supervisor(stub: StubServer, tmp_path: Path) -> None:
    stub.on("POST", f"{BASE}/credential", ISSUED)
    directory = tmp_path / "rc-1"

    token_script = host_unit(stub, directory).ensure()

    assert token_script.is_file()
    assert stub.requests("PUT", BASE)[0].auth == "Bearer sup-token"
    assert stub.requests("POST", f"{BASE}/credential")[0].auth == "Bearer sup-token"
    assert stub.requests("GET", "/api/workspace/update_script")[0].auth == "Bearer issued-access"
    assert "issued-refresh" in (directory / ".slop-haus" / "credentials.json").read_text()
