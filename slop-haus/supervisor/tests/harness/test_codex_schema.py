"""The `app-server` protocol is experimental: regenerate the schema of the installed CLI and hold the parts the harness reads.

Skipped when `codex` is absent. A different CLI version than CODEX_VERSION fails, so a bump is a deliberate edit of both.
"""

from __future__ import annotations

import json
import shutil
import subprocess
from pathlib import Path

import pytest

from slop_haus_supervisor.harness.codex import CODEX_VERSION
from slop_haus_supervisor.harness.codex_protocol import DECLINES

pytestmark = pytest.mark.skipif(shutil.which("codex") is None, reason="codex is not installed")


@pytest.fixture(scope="module")
def schema(tmp_path_factory: pytest.TempPathFactory) -> Path:
    out = tmp_path_factory.mktemp("codex_schema")
    version = subprocess.run(["codex", "--version"], capture_output=True, text=True, check=True).stdout.strip()
    assert version == f"codex-cli {CODEX_VERSION}", f"installed {version}; the harness is written for {CODEX_VERSION}"
    subprocess.run(["codex", "app-server", "generate-json-schema", "--out", str(out)], capture_output=True, check=True)
    return out


def load(schema: Path, name: str) -> dict[str, object]:
    return json.loads((schema / name).read_text(encoding="utf-8"))


def methods(schema: Path, name: str) -> set[str]:
    return {variant["properties"]["method"]["enum"][0] for variant in load(schema, name)["oneOf"]}  # pyright: ignore[reportUnknownVariableType, reportGeneralTypeIssues]


def properties(schema: Path, name: str) -> dict[str, object]:
    return load(schema, name)["properties"]  # pyright: ignore[reportReturnType]


def test_the_requests_the_harness_sends_exist(schema: Path) -> None:
    assert {"initialize", "thread/start", "thread/resume", "turn/start"} <= methods(schema, "ClientRequest.json")
    assert "initialized" in methods(schema, "ClientNotification.json")


def test_the_params_it_sends_exist(schema: Path) -> None:
    assert {"cwd", "approvalPolicy", "sandbox", "config", "developerInstructions", "model"} <= set(properties(schema, "v2/ThreadStartParams.json"))
    assert {"threadId", "config", "developerInstructions", "model", "cwd", "approvalPolicy", "sandbox"} <= set(properties(schema, "v2/ThreadResumeParams.json"))
    assert {"threadId", "input", "effort"} <= set(properties(schema, "v2/TurnStartParams.json"))


def test_the_notifications_it_reads_exist_with_their_fields(schema: Path) -> None:
    assert {"turn/completed", "error"} <= methods(schema, "ServerNotification.json")
    assert {"threadId", "turn"} <= set(properties(schema, "v2/TurnCompletedNotification.json"))
    assert {"error", "willRetry"} <= set(properties(schema, "v2/ErrorNotification.json"))
    turn = load(schema, "v2/TurnCompletedNotification.json")["definitions"]["Turn"]["properties"]  # pyright: ignore[reportUnknownVariableType, reportIndexIssue]
    assert {"status", "error"} <= set(turn)  # pyright: ignore[reportUnknownArgumentType]


def test_every_server_request_has_a_deliberate_answer(schema: Path) -> None:
    # A new kind of ask would be answered with a method-not-found error; this lists the ones that are not declined.
    unanswered = {"item/tool/call", "account/chatgptAuthTokens/refresh", "attestation/generate"}
    assert methods(schema, "ServerRequest.json") == set(DECLINES) | unanswered


def test_the_declines_have_the_shapes_the_schema_wants(schema: Path) -> None:
    assert "decline" in json.dumps(load(schema, "CommandExecutionRequestApprovalResponse.json"))
    assert "decline" in json.dumps(load(schema, "FileChangeRequestApprovalResponse.json"))
    assert "denied" in json.dumps(load(schema, "ApplyPatchApprovalResponse.json"))
    assert "denied" in json.dumps(load(schema, "ExecCommandApprovalResponse.json"))
    assert "answers" in properties(schema, "ToolRequestUserInputResponse.json")
    assert "action" in properties(schema, "McpServerElicitationRequestResponse.json")
    assert {"permissions"} <= set(properties(schema, "PermissionsRequestApprovalResponse.json"))
