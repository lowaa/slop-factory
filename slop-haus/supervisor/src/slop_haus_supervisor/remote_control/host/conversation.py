from __future__ import annotations

import dataclasses
import json
import os
import tempfile
from collections import deque
from dataclasses import dataclass, field
from typing import Any

from slop_haus_supervisor.remote_control.host.activity import CallLines
from slop_haus_supervisor.remote_control.host.live_harness import LiveHarness
from slop_haus_supervisor.usage.conversation import ConversationUsage


@dataclass
class ConversationState:
    """What survives a restart of the host: enough to resume a conversation
    and to keep its usage totals."""

    id: str
    work_seq: int = 0
    # The harness has a session of this id on this machine: resume it.
    created: bool = False
    # The id the harness knows the session by, when it is not the conversation's: a clear starts
    # a session under a new one and never resumes the old.
    session: str = ""
    # The newest clear done, so one the server sends again is not done twice.
    clear_seq: int = 0
    # The newest compact done, for the same reason.
    compact_seq: int = 0
    # The input side of the newest turn's usage, as last measured; 0 is unknown.
    context_tokens: int = 0
    turns: int = 0
    # The newest message handed to the harness, so one the server sends again
    # after a restart is not asked twice.
    last_message_seq: int = 0
    billing_mode: str = "subscription"
    usage: dict[str, Any] = field(default_factory=lambda: {})
    # The commit the working copy was at when this host first saw the
    # conversation: what its changes are counted from.
    base: str = ""
    # Paths of what the conversation pushed: a pushed commit is on a remote and
    # no longer shows as unpushed, but is still the conversation's work.
    pushed_paths: list[str] = field(default_factory=lambda: [])
    # pi only: the session totals its stats last reported, which a turn's usage is the change
    # from (pi's are for the whole session, whichever process), and the model the last turn ran on.
    pi_stats: dict[str, int] = field(default_factory=lambda: {})
    pi_provider: str = ""
    pi_model: str = ""
    # Codex only: the thread totals its usage notifications last accounted for, which a turn's usage is
    # the change from (a thread's are for the whole thread, whichever process).
    codex_totals: dict[str, int] = field(default_factory=lambda: {})
    # Ids of the attachments fetched into the working copy for this conversation, to remove with it.
    attachments: list[str] = field(default_factory=lambda: [])


    @property
    def session_id(self) -> str:
        return self.session or self.id


@dataclass
class WorkItem:
    seq: int
    type: str
    conversation: str
    raw: dict[str, Any]


def atomic_write(path: str, text: str) -> None:
    directory = os.path.dirname(path)
    os.makedirs(directory, mode=0o700, exist_ok=True)
    fd, tmp = tempfile.mkstemp(dir=directory, prefix=".tmp-")
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as f:
            f.write(text)
        os.replace(tmp, path)
    except BaseException:
        try:
            os.unlink(tmp)
        except OSError:
            pass
        raise


class StateStore:
    """The conversations' state files, one JSON file each."""

    def __init__(self, directory: str) -> None:
        self.directory = directory

    def path(self, conversation: str) -> str:
        return os.path.join(self.directory, conversation + ".json")

    def load(self, conversation: str) -> ConversationState | None:
        try:
            with open(self.path(conversation), encoding="utf-8") as f:
                data = json.load(f)
            known = {f.name for f in dataclasses.fields(ConversationState)}
            data = {k: v for k, v in data.items() if k in known}
            data["id"] = conversation
            return ConversationState(**data)
        except (OSError, ValueError, TypeError, AttributeError):
            return None

    def save(self, state: ConversationState) -> None:
        atomic_write(self.path(state.id), json.dumps(dataclasses.asdict(state)))


class Conversation:
    def __init__(self, state: ConversationState, store: StateStore) -> None:
        self.state = state
        self.store = store
        self.usage = ConversationUsage.resume(state.usage)
        self.live: LiveHarness | None = None
        self.generation = 0
        self.turn_active = False
        self.current_text = ""
        self.pending: deque[tuple[str, int]] = deque()
        # The message the turn in flight answers, held until the turn has a result.
        self.in_flight_seq: int | None = None
        self.idle_since: float | None = None
        # Text streamed so far per message id and content block index, so the finished
        # message sends only what the deltas did not.
        self.streamed: dict[str, dict[Any, str]] = {}
        self.streaming_id: str | None = None
        self.expected_exit = False
        # The owner asked for the turn in flight to stop, so its end is "stopped by you", not an
        # error; the deadline is when the host gives up waiting for the harness to do it.
        self.interrupt_requested = False
        self.interrupt_deadline: float | None = None
        self.retried_session = False
        # The turn in flight is a compaction, and why it did not work when the harness said so.
        self.compacting = False
        self.compact_error = ""
        # What the server was last told of the context size (None: nothing yet by this host).
        self.reported_context: int | None = None
        self.tool_uses: dict[str, tuple[str, str]] = {}
        # The tool calls in flight, as the board is told of them.
        self.calls = CallLines()
        # The repo as the turn in flight began.
        self.snapshot: dict[str, Any] | None = None

    def save(self) -> None:
        self.state.usage = self.usage.dump()
        self.store.save(self.state)
