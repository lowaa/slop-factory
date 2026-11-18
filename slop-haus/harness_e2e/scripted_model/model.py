"""The scripted model: hands out a scenario's turns and records what it was asked.

API-independent. An adapter (apis/) turns a wire request into a ModelRequest
and a Turn back into a wire response; this decides which Turn.
"""

from __future__ import annotations

import threading
from dataclasses import dataclass, field
from typing import Any, Callable

from .scenario import Scenario, Turn


@dataclass(frozen=True)
class ModelRequest:
    """What an adapter extracts from one wire request."""

    api: str
    model: str
    system: str
    tool_names: tuple[str, ...]
    messages: tuple[Any, ...]
    raw: dict[str, Any]
    # Every piece of text the harness sent in its messages, tool results
    # included, and the tool results alone: what assertions about "what the
    # model was shown" read, whatever the wire API's shape.
    message_text: str = ""
    tool_results: tuple[str, ...] = ()
    # A request the harness makes for itself (a quota probe, a title), not a
    # step of the conversation under test: answered without consuming the
    # script, and kept out of `conversation` in the recording.
    auxiliary: bool = False


class UnexpectedRequest(Exception):
    """A request the scenario did not expect. The message carries the request."""


@dataclass
class Recording:
    requests: list[ModelRequest] = field(default_factory=list)
    errors: list[str] = field(default_factory=list)

    def conversation(self) -> list[ModelRequest]:
        return [r for r in self.requests if not r.auxiliary]


class ScriptedModel:
    def __init__(self, summarise: Callable[[ModelRequest], str]) -> None:
        self._lock = threading.Lock()
        self._summarise = summarise
        self._scenario: Scenario | None = None
        self._next = 0
        self.recording = Recording()
        self._gates: dict[str, threading.Event] = {}

    def gate(self, name: str) -> threading.Event:
        with self._lock:
            return self._gates.setdefault(name, threading.Event())

    def _release_waiters(self) -> None:
        # A request still held at a gate belongs to the scenario being replaced:
        # let it go now rather than have it time out into the next one's errors.
        for gate in self._gates.values():
            gate.set()
        self._gates = {}

    def load(self, scenario: Scenario) -> None:
        with self._lock:
            self._release_waiters()
            self._scenario = scenario
            self._next = 0
            self.recording = Recording()

    def reset(self) -> None:
        with self._lock:
            self._release_waiters()
            self._scenario = None
            self._next = 0
            self.recording = Recording()

    def answer(self, request: ModelRequest) -> Turn | None:
        """The turn for a conversation request; None for an auxiliary one."""
        return self.answer_gated(request)[0]

    def answer_gated(self, request: ModelRequest) -> tuple[Turn | None, threading.Event | None]:
        """answer, and the gate the turn waits at, taken in the same step so a
        scenario replaced in between cannot hand the request a gate of the next."""
        with self._lock:
            self.recording.requests.append(request)
            if request.auxiliary:
                return None, None
            if self._scenario is None:
                raise self._fail("no scenario is loaded", request)
            if self._next >= len(self._scenario.turns):
                raise self._fail(
                    f"scenario {self._scenario.name!r} has only {len(self._scenario.turns)} turn(s), and the harness asked for another",
                    request,
                )
            turn = self._scenario.turns[self._next]
            missing = [c.name for c in turn.tool_calls if c.name not in request.tool_names]
            if missing:
                raise self._fail(
                    f"turn {self._next + 1} of scenario {self._scenario.name!r} calls tool(s) {missing} that the harness did not offer",
                    request,
                )
            self._next += 1
            return turn, (self._gates.setdefault(turn.gate, threading.Event()) if turn.gate else None)

    def fail(self, message: str, request: ModelRequest | None = None) -> UnexpectedRequest:
        with self._lock:
            return self._fail(message, request)

    def _fail(self, message: str, request: ModelRequest | None) -> UnexpectedRequest:
        detail = f"{message}\n{self._summarise(request)}" if request else message
        self.recording.errors.append(detail)
        return UnexpectedRequest(detail)

    def remaining(self) -> int:
        with self._lock:
            return 0 if self._scenario is None else len(self._scenario.turns) - self._next
