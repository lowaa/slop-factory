"""Turns the hook payloads a real Claude Code sent into fixtures that do not change from one
capture to the next: the fields a run makes up (session id, paths) are replaced by fixed values,
and every other field is kept as Claude Code sent it, which is the point of a fixture."""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any

SESSION_ID = "00000000-0000-4000-8000-000000000001"
CWD = "/work/space"
TRANSCRIPT = f"/home/user/.claude/projects/-work-space/{SESSION_ID}.jsonl"

# Fields whose value belongs to the run that sent them.
FIXED: dict[str, Any] = {"prompt_id": "00000000-0000-4000-8000-000000000002", "session_id": SESSION_ID, "cwd": CWD, "transcript_path": TRANSCRIPT}


def sanitized(payload: dict[str, Any], workspace: Path) -> dict[str, Any]:
    out = {}
    for key, value in payload.items():
        if key in FIXED:
            out[key] = FIXED[key]
        elif isinstance(value, str):
            out[key] = value.replace(str(workspace), CWD)
        else:
            out[key] = value
    return out


def write(directory: Path, name: str, payload: dict[str, Any]) -> None:
    directory.mkdir(parents=True, exist_ok=True)
    (directory / f"{name}.json").write_text(json.dumps(payload, indent=2, sort_keys=True) + "\n")
