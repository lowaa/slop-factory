from __future__ import annotations

import fcntl
import json
import logging
import sys
import threading
from collections.abc import Generator
from pathlib import Path

import pytest

sys.path.insert(0, str(Path(__file__).parent.parent / "slophaus"))

from slop_haus_supervisor.ports import SubprocessRunner
from slop_haus_supervisor.slophaus.supervisor_api import SupervisorApi
from slop_haus_supervisor.slophaus.transport import Transport
from slop_haus_supervisor.steward.enrollment import StewardEnrollment
from slop_haus_supervisor.workspace.config import ConfigError
from slop_haus_supervisor.workspace.provisioned import Kind, Provisioned
from stub_server import StubServer, serving

BASE = "/api/supervisor/steward"
ISSUED = (200, {"access_token": "issued-access", "refresh_token": "issued-refresh", "expires_in": 900})
REFRESHED = (200, {"access_token": "fresh-access", "refresh_token": "fresh-refresh", "expires_in": 900})

# The server's update script refuses a directory without .slop-haus.env (see the guard at the top of
# server/internal/workspace's golden update scripts) and otherwise lays the helper files down.
UPDATE_SCRIPT = """#!/usr/bin/env bash
set -euo pipefail
if [ ! -f '.slop-haus.env' ]; then
  echo "slop-haus: $(pwd) is not a slop-haus workspace (no .slop-haus.env) -- run setup there instead" >&2
  exit 1
fi
mkdir -p .slop-haus
printf '#!/bin/sh\\n' > .slop-haus/token.sh
"""


@pytest.fixture
def stub() -> Generator[StubServer]:
    s = StubServer()
    s.on("PUT", BASE, (200, {"id": "s1", "name": "steward"}))
    s.on("GET", "/api/workspace/update_script", (200, UPDATE_SCRIPT))
    with serving(s):
        yield s


def steward_unit(stub: StubServer, directory: Path) -> Provisioned:
    api = SupervisorApi(Transport(stub.url), lambda: "sup-token")
    return Provisioned(directory, Kind.STEWARD, SubprocessRunner(), logging.getLogger("test"), StewardEnrollment(api, stub.url))


def leave_credentials(directory: Path, access: str = "old-access", refresh: str = "old-refresh") -> None:
    state = directory / ".slop-haus"
    state.mkdir(parents=True)
    (state / "credentials.json").write_text(json.dumps({"access_token": access, "refresh_token": refresh, "expires_at": 1}))
    (state / "server-url").write_text("x\n")


def stored_refresh(directory: Path) -> str:
    return json.loads((directory / ".slop-haus" / "credentials.json").read_text())["refresh_token"]


def test_a_fresh_steward_directory_ends_up_with_the_managed_files(stub: StubServer, tmp_path: Path) -> None:
    stub.on("POST", f"{BASE}/credential", ISSUED)
    directory = tmp_path / "steward"

    token_script = steward_unit(stub, directory).ensure()

    assert token_script.is_file()
    assert (directory / ".slop-haus.env").read_text() == "SLOP_HAUS_AGENT=steward\n"
    assert (directory / ".slop-haus" / "no-clone").is_file()
    assert stored_refresh(directory) == "issued-refresh"
    assert stub.requests("GET", "/api/workspace/update_script")[0].auth == "Bearer issued-access"


def test_credentials_without_the_helper_are_repaired_with_the_credential_it_has(stub: StubServer, tmp_path: Path) -> None:
    stub.on("POST", "/oauth/token", REFRESHED)
    directory = tmp_path / "steward"
    leave_credentials(directory)

    token_script = steward_unit(stub, directory).ensure()

    assert token_script.is_file()
    assert stub.requests("POST", f"{BASE}/credential") == []
    assert stub.requests("POST", f"{BASE}/credential/revoke") == []
    assert stored_refresh(directory) == "fresh-refresh"
    assert stub.requests("GET", "/api/workspace/update_script")[0].auth == "Bearer fresh-access"
    assert "refresh_token=old-refresh" in stub.requests("POST", "/oauth/token")[0].body


def test_a_refresh_that_fails_for_another_reason_than_a_dead_credential_issues_nothing(stub: StubServer, tmp_path: Path) -> None:
    stub.on("POST", "/oauth/token", (503, {"detail": "down"}))
    directory = tmp_path / "steward"
    leave_credentials(directory)

    with pytest.raises(ConfigError):
        steward_unit(stub, directory).ensure()

    assert stub.requests("POST", f"{BASE}/credential") == []
    assert stored_refresh(directory) == "old-refresh"


def test_replacing_a_dead_credential_revokes_the_old_grant(stub: StubServer, tmp_path: Path) -> None:
    stub.on("POST", "/oauth/token", (400, {"error": "invalid_grant"}))
    stub.on("POST", f"{BASE}/credential", ISSUED)
    stub.on("POST", f"{BASE}/credential/revoke", (200, ""))
    directory = tmp_path / "steward"
    leave_credentials(directory)

    steward_unit(stub, directory).ensure()

    assert len(stub.requests("POST", f"{BASE}/credential")) == 1
    assert json.loads(stub.requests("POST", f"{BASE}/credential/revoke")[0].body) == {"refresh_token": "old-refresh"}
    assert stored_refresh(directory) == "issued-refresh"


def test_a_revoke_the_server_refuses_does_not_undo_the_replacement(stub: StubServer, tmp_path: Path) -> None:
    stub.on("POST", "/oauth/token", (401, {"detail": "revoked"}))
    stub.on("POST", f"{BASE}/credential", ISSUED)
    stub.on("POST", f"{BASE}/credential/revoke", (403, {"detail": "gone"}))
    directory = tmp_path / "steward"
    leave_credentials(directory)

    assert steward_unit(stub, directory).ensure().is_file()
    assert stored_refresh(directory) == "issued-refresh"


def test_a_complete_directory_is_left_alone(stub: StubServer, tmp_path: Path) -> None:
    directory = tmp_path / "steward"
    leave_credentials(directory)
    (directory / ".slop-haus" / "token.sh").write_text("#!/bin/sh\n")

    steward_unit(stub, directory).ensure()

    assert stub.seen == []


def test_a_repair_waits_for_the_credentials_lock_and_refreshes_the_pair_stored_when_it_gets_it(stub: StubServer, tmp_path: Path) -> None:
    stub.on("POST", "/oauth/token", REFRESHED)
    directory = tmp_path / "steward"
    leave_credentials(directory)
    holder = open(directory / ".slop-haus" / "credentials.lock", "w")
    fcntl.flock(holder, fcntl.LOCK_EX)
    done = threading.Event()
    result: list[Path] = []

    def repair() -> None:
        result.append(steward_unit(stub, directory).ensure())
        done.set()

    worker = threading.Thread(target=repair)
    worker.start()
    try:
        assert not done.wait(0.5), "the repair went ahead while another process held the lock"
        assert stub.requests("POST", "/oauth/token") == []
        # What a concurrent token.sh would have left: the pair the repair must spend, not the one it read before.
        (directory / ".slop-haus" / "credentials.json").write_text(
            json.dumps({"access_token": "newer-access", "refresh_token": "newer-refresh", "expires_at": 1})
        )
    finally:
        fcntl.flock(holder, fcntl.LOCK_UN)
        holder.close()
    worker.join(30)

    assert done.is_set()
    assert "refresh_token=newer-refresh" in stub.requests("POST", "/oauth/token")[0].body
    assert stored_refresh(directory) == "fresh-refresh"


DEAD_HELPER = """#!/bin/sh
echo "slop-haus: this workspace's credentials were rejected -- run 'token.sh login'" >&2
exit 1
"""


def leave_dead_steward(directory: Path) -> None:
    leave_credentials(directory)
    (directory / ".slop-haus" / "token.sh").write_text(DEAD_HELPER)
    (directory / ".slop-haus" / "token.sh").chmod(0o755)


def test_a_rejected_credential_with_a_helper_in_place_is_replaced_once_over_many_runs(stub: StubServer, tmp_path: Path) -> None:
    stub.on("POST", f"{BASE}/credential", ISSUED)
    directory = tmp_path / "steward"
    leave_dead_steward(directory)
    unit = steward_unit(stub, directory)

    for _ in range(5):
        unit.ensure()

    assert len(stub.requests("POST", f"{BASE}/credential")) == 1
    assert stub.requests("POST", f"{BASE}/credential/revoke") == [], "the grant is already dead; there is nothing to end"
    assert stored_refresh(directory) == "issued-refresh"


def test_replacements_that_are_rejected_too_stop_after_one_until_the_supervisor_restarts(stub: StubServer, tmp_path: Path) -> None:
    stub.on("POST", f"{BASE}/credential", ISSUED)
    directory = tmp_path / "steward"
    leave_dead_steward(directory)
    unit = steward_unit(stub, directory)
    # The update script lays down a helper that is as dead as the one it replaces.
    stub.on("GET", "/api/workspace/update_script", (200, UPDATE_SCRIPT + f"cat > .slop-haus/token.sh <<'EOF'\n{DEAD_HELPER}EOF\nchmod +x .slop-haus/token.sh\n"))

    for _ in range(5):
        with pytest.raises(ConfigError):
            unit.ensure()

    assert len(stub.requests("POST", f"{BASE}/credential")) == 1

    restarted = steward_unit(stub, directory)
    with pytest.raises(ConfigError):
        restarted.ensure()
    assert len(stub.requests("POST", f"{BASE}/credential")) == 2


def test_a_helper_that_fails_for_another_reason_does_not_replace_the_credential(stub: StubServer, tmp_path: Path) -> None:
    directory = tmp_path / "steward"
    leave_dead_steward(directory)
    (directory / ".slop-haus" / "token.sh").write_text("#!/bin/sh\necho 'could not reach the server' >&2\nexit 1\n")
    (directory / ".slop-haus" / "token.sh").chmod(0o755)

    steward_unit(stub, directory).ensure()

    assert stub.seen == []
