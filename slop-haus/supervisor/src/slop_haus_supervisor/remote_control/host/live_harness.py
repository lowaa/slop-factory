from __future__ import annotations

import queue
import threading
from dataclasses import dataclass
from typing import Any

from slop_haus_supervisor.harness.process import HarnessSession


@dataclass
class HarnessEvent:
    conversation: str
    generation: int
    # A decoded record from the harness, or None when it has exited.
    record: dict[str, Any] | None
    exit_code: int | None = None


class LiveHarness:
    """One harness process of a conversation, and what the host has learnt of it.

    Its records and its exit are put on the host's inbox, so that everything which changes
    state runs on the host's one thread.
    """

    def __init__(self, session: HarnessSession, conversation: str, generation: int,
                 inbox: queue.Queue[Any], started: bool = False) -> None:
        self.session = session
        self.conversation = conversation
        self.generation = generation
        self.inbox = inbox
        # Whether the process got as far as a session (Claude Code's init record). True from the
        # start for a harness with no session to guess about.
        self.started = started
        # A result before init: Claude Code's answer to a --resume of a session it
        # does not have, before it exits. The exit says whether the host guessed wrongly.
        self.startup_result: dict[str, Any] | None = None
        threading.Thread(target=self._pump, daemon=True).start()

    def _pump(self) -> None:
        while True:
            record = self.session.next_record()
            if record is None:
                break
            self.inbox.put(HarnessEvent(self.conversation, self.generation, record))
        self.inbox.put(HarnessEvent(self.conversation, self.generation, None, exit_code=self.session.exit_code()))

    def send(self, text: str) -> None:
        self.session.send(text)

    def write(self, line: str) -> None:
        """A protocol line of the harness's own, not a user message."""
        self.session.send_line(line)

    def interrupt(self) -> bool:
        return self.session.interrupt()

    def stderr_tail(self) -> str:
        return self.session.stderr_tail()

    def terminate(self) -> None:
        self.session.close()
