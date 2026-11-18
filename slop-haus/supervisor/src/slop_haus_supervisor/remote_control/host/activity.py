from __future__ import annotations

import posixpath

from typing import Any

from slop_haus_supervisor.harness.text import as_dict
from slop_haus_supervisor.text import clean_field

# The board shows a call as one short line; the server accepts more (480 bytes), so a
# cut here never makes it refuse the event.
MAX_LINE_CHARS = 120
MAX_TOOL_BYTES = 64
MAX_CALL_ID_BYTES = 128

RUNNING = "running"
DONE = "done"
FAILED = "failed"

# Tools whose input carries a line the model wrote to describe the call.
DESCRIBED_TOOLS = ("Bash", "Task", "Agent")
# Tools whose input names the file they work on. Only the file's base name is shown.
FILE_TOOLS = {"Read": "file_path", "Edit": "file_path", "MultiEdit": "file_path", "Write": "file_path", "NotebookEdit": "notebook_path"}


def activity_line(text: str) -> str:
    """One call's line as it goes to the board: single-line, free of control characters and of
    credentials in URLs, and short."""
    return clean_field(" ".join(text.split()), 4 * MAX_LINE_CHARS)[:MAX_LINE_CHARS].strip()


def base_name(path: object) -> str:
    return activity_line(posixpath.basename(path.replace("\\", "/"))) if isinstance(path, str) else ""


def claude_line(tool: str, tool_input: object) -> str:
    """The line for a Claude Code tool call: the model's own description where the tool has one, a
    file's base name for the file tools. Never the command, the pattern, the content or anything
    else of the input, which is where secrets and personal data are."""
    given = as_dict(tool_input) or {}
    if tool in DESCRIBED_TOOLS:
        description = given.get("description")
        return activity_line(description) if isinstance(description, str) else ""
    if tool in FILE_TOOLS:
        return base_name(given.get(FILE_TOOLS[tool]))
    return ""


def tool_name(name: object) -> str:
    return clean_field(str(name or ""), MAX_TOOL_BYTES).strip() or "tool"


class CallLines:
    """The calls of the turn in flight, so the report that a call ended repeats the line it began
    with instead of the host keeping the call's input."""

    def __init__(self) -> None:
        self.calls: dict[str, tuple[str, str]] = {}

    def start(self, call_id: str, tool: str, line: str) -> tuple[str, str]:
        self.calls[call_id] = (tool, line)
        return tool, line

    def end(self, call_id: str, default_tool: str = "tool") -> tuple[str, str]:
        return self.calls.pop(call_id, (default_tool, ""))

    def abandon(self) -> list[tuple[str, str, str]]:
        """The calls that never got a result, as (id, tool, line), forgotten."""
        left = [(call_id, tool, line) for call_id, (tool, line) in self.calls.items()]
        self.calls.clear()
        return left


def fail_open_calls(conv: Any, reporter: Any) -> None:
    """A turn or a process ended with calls still running (an interrupt, a crash): the board is told
    they failed, or they would show as running for ever."""
    for call_id, tool, line in conv.calls.abandon():
        reporter.activity(conv.state.id, call_id, tool, line, FAILED)


def call_id_of(value: object) -> str:
    return clean_field(str(value), MAX_CALL_ID_BYTES)
