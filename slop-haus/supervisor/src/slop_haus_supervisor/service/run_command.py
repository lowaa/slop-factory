from __future__ import annotations

from pathlib import Path

from slop_haus_supervisor.workspace.layout import Layout

LOG_NAME = "supervisor.log"


def log_file(layout: Layout) -> Path:
    return layout.root / LOG_NAME


def run_argv(uv: str, layout: Layout) -> list[str]:
    """The command every launcher runs: `slop-haus start` in a multiplexer and `service install` in a unit, so they cannot drift."""
    return [
        uv, "run", "--frozen", "--no-dev", "--project", str(layout.app),
        "slop-haus-supervisor", "run", "--root", str(layout.root), "--log-file", str(log_file(layout)),
    ]  # fmt: skip
