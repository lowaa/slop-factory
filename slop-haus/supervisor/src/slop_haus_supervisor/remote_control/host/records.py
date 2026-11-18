from __future__ import annotations

from typing import Any, Protocol

from slop_haus_supervisor.remote_control.host.conversation import Conversation
from slop_haus_supervisor.remote_control.host.live_harness import LiveHarness


class TextReporter(Protocol):
    def text(self, conversation: str, text: str, block: str = "") -> int: ...

    def activity(self, conversation: str, call_id: str, tool: str, line: str, status: str) -> int: ...


class RecordHandler(Protocol):
    """What a harness's records mean to a conversation, and the few places its wording or
    start-up differs. Claude Code's is ClaudeRecords; pi's is PiRpc."""

    # Whether a start-up refusal can mean the host guessed wrongly whether the session exists.
    # pi's --session-id creates a missing session, so for it there is no wrong guess.
    retries_session: bool
    # Whether the harness has a command to compact its context. The board offers Compact only to a
    # host that says so.
    can_compact: bool

    def on_record(self, conv: Conversation, live: LiveHarness, record: dict[str, Any]) -> None: ...

    def compact(self, conv: Conversation, live: LiveHarness) -> None:
        """Asks the harness to compact the session's context, by its own command. The compaction
        is a turn: it ends the way a turn does, and `conv.compacting` says it is one."""
        ...

    def explain(self, text: str) -> str:
        """An error shown to the owner, with whatever the harness's own words leave out."""
        ...

    def describe_exit(self, stderr: str) -> str:
        """A harness's stderr as the owner is told it, once it has exited."""
        ...
