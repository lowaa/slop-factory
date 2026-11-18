"""A live `codex app-server` for a caller that owns the conversation (Remote Control): one process, many turns."""

from __future__ import annotations

import json
import logging
import threading
from collections import deque
from collections.abc import Callable, Mapping
from typing import Any

from slop_haus_supervisor.harness.codex_protocol import DECLINES, METHOD_NOT_FOUND, NO_ROLLOUT, is_uuid
from slop_haus_supervisor.harness.process import HarnessProcess, HarnessSession
from slop_haus_supervisor.harness.text import as_dict

log = logging.getLogger(__name__)

# Notifications of this session's own, put among Codex's so the caller reads one stream.
THREAD_READY = "slop-haus/threadReady"
SESSION_ERROR = "slop-haus/error"
# What turn/interrupt answers when the turn had already ended: a lost race, not a failure.
NO_ACTIVE_TURN = "no active turn to interrupt"


class CodexSession(HarnessSession):
    """The handshake, and which of turn/start and turn/steer a message is.

    Everything else Codex says is passed on as it came, for the caller's RecordHandler. A
    `thread/resume` of a thread Codex has no rollout for starts a new thread, which THREAD_READY
    says with `fresh`; a request Codex refused is SESSION_ERROR.
    """

    def __init__(self, process: HarnessProcess, thread_params: Mapping[str, Any], resume_id: str, effort: str,
                 save_thread: Callable[[str], None], session_id: str, grace: float = 5.0, fresh: bool = False) -> None:
        super().__init__(process, lambda text: text, session_id, grace)
        self._params = dict(thread_params)
        self._resume_id = resume_id
        self._effort = effort
        self._save_thread = save_thread
        # The caller meant to resume but has no thread to resume.
        self._fresh = fresh
        # Guards the protocol state below: the caller's thread sends, another reads.
        self._wire = threading.Lock()
        self._next_id = 0
        self._requests: dict[int, tuple[str, str]] = {}
        self._thread_id = ""
        self._turn_id = ""
        self._turn_starting = False
        self._queued: deque[str] = deque()
        # A compaction asked for before the thread was ready to take it.
        self._compact_queued = False
        self._out: deque[dict[str, Any]] = deque()
        with self._wire:
            self._request("initialize", "initialize", {
                "clientInfo": {"name": "slop-haus-supervisor", "title": "slop-haus", "version": "1"},
                "capabilities": {},
            })

    @property
    def thread_id(self) -> str:
        with self._wire:
            return self._thread_id

    def send(self, text: str) -> None:
        with self._wire:
            self._dispatch(text)

    def compact(self) -> None:
        """Starts a compaction of the thread, which Codex runs as a turn of its own."""
        with self._wire:
            if not self._thread_id or self._turn_starting:
                self._compact_queued = True
            else:
                self._request("compact", "thread/compact/start", {"threadId": self._thread_id})

    def interrupt(self) -> bool:
        """Cancels the turn in flight; a turn that has just ended is a harmless race. False when Codex has
        not named the turn yet, so there is nothing to cancel it by and the caller must end the process."""
        with self._wire:
            if not (self._thread_id and self._turn_id):
                return False
            self._request("interrupt", "turn/interrupt", {"threadId": self._thread_id, "turnId": self._turn_id})
        return True

    def next_record(self) -> dict[str, Any] | None:
        while True:
            with self._wire:
                if self._out:
                    return self._out.popleft()
            record = self._process.next_record()
            if record is None:
                return None
            try:
                with self._wire:
                    self._handle(record)
            except RuntimeError:
                # close() ended the session while this record was being answered.
                return None

    # -- requests (the wire lock is held) --

    def _request(self, kind: str, method: str, params: dict[str, Any], payload: str = "") -> None:
        self._next_id += 1
        self._requests[self._next_id] = (kind, payload)
        self._write(json.dumps({"id": self._next_id, "method": method, "params": params}))

    def _dispatch(self, text: str) -> None:
        if not self._thread_id or self._turn_starting:
            self._queued.append(text)
        elif self._turn_id:
            self._request("steer", "turn/steer", {
                "threadId": self._thread_id, "expectedTurnId": self._turn_id, "input": [{"type": "text", "text": text}],
            }, text)
        else:
            self._start_turn(text)

    def _start_turn(self, text: str) -> None:
        params: dict[str, Any] = {"threadId": self._thread_id, "input": [{"type": "text", "text": text}]}
        if self._effort:
            params["effort"] = self._effort
        self._turn_starting = True
        self._request("turn", "turn/start", params)

    def _flush(self) -> None:
        queued, self._queued = list(self._queued), deque()
        for text in queued:
            self._dispatch(text)
        if self._compact_queued and self._thread_id and not self._turn_starting:
            self._compact_queued = False
            self._request("compact", "thread/compact/start", {"threadId": self._thread_id})

    def _fail(self, message: str) -> None:
        self._out.append({"method": SESSION_ERROR, "params": {"message": message}})

    # -- records (the wire lock is held) --

    def _handle(self, record: dict[str, Any]) -> None:
        method, rid = record.get("method"), record.get("id")
        if isinstance(method, str):
            if rid is not None:
                self._refuse(method, rid)
            else:
                self._on_notification(method, record)
        elif ("result" in record or "error" in record) and isinstance(rid, int) and rid in self._requests:
            kind, payload = self._requests.pop(rid)
            self._on_response(kind, payload, as_dict(record.get("error")), as_dict(record.get("result")) or {})

    def _refuse(self, method: str, rid: object) -> None:
        """Nobody is at a terminal: unanswered, an ask would hold the turn for ever."""
        log.warning("codex asked for %s; declined", method)
        decline = DECLINES.get(method)
        if decline is not None:
            self._write(json.dumps({"id": rid, "result": decline}))
        else:
            self._write(json.dumps({"id": rid, "error": {"code": METHOD_NOT_FOUND, "message": f"{method} is not supported by this client"}}))

    def _on_notification(self, method: str, record: dict[str, Any]) -> None:
        params = as_dict(record.get("params")) or {}
        thread = params.get("threadId")
        if isinstance(thread, str) and self._thread_id and thread != self._thread_id:
            return
        if method == "turn/started":
            turn_id = (as_dict(params.get("turn")) or {}).get("id")
            if isinstance(turn_id, str):
                self._turn_id = turn_id
        elif method == "turn/completed":
            self._turn_id = ""
            if self._queued and not self._turn_starting:
                self._start_turn(self._queued.popleft())
        self._out.append(record)

    def _on_response(self, kind: str, payload: str, error: dict[str, Any] | None, result: dict[str, Any]) -> None:
        message = str(error.get("message") or "codex refused the request") if error is not None else ""
        if kind == "initialize":
            if error is not None:
                self._fail(message)
                return
            self._write(json.dumps({"method": "initialized"}))
            self._open_thread()
        elif kind == "thread":
            self._on_thread(error, message, result)
        elif kind == "turn":
            self._turn_starting = False
            if error is not None:
                self._fail(message)
                return
            turn_id = (as_dict(result.get("turn")) or {}).get("id")
            if isinstance(turn_id, str):
                self._turn_id = turn_id
            self._flush()
        elif kind == "compact" and error is not None:
            self._fail(message)
        elif kind == "steer" and error is not None:
            # The turn ended between the host's check and Codex's: the message is a turn of its own.
            log.info("codex refused a steer (%s); starting a turn instead", message)
            if not self._turn_starting:
                self._start_turn(payload)
        elif kind == "interrupt" and error is not None and NO_ACTIVE_TURN not in message:
            log.warning("codex refused an interrupt: %s", message)

    def _open_thread(self) -> None:
        if self._resume_id:
            self._request("thread", "thread/resume", {**self._params, "threadId": self._resume_id})
        else:
            self._request("thread", "thread/start", self._params)

    def _on_thread(self, error: dict[str, Any] | None, message: str, result: dict[str, Any]) -> None:
        if error is not None:
            if self._resume_id and NO_ROLLOUT in message:
                self._resume_id, self._fresh = "", True
                self._open_thread()
            else:
                self._fail(message)
            return
        thread_id = (as_dict(result.get("thread")) or {}).get("id")
        if not isinstance(thread_id, str) or not is_uuid(thread_id):
            self._fail("codex answered the thread request without a usable thread id")
            return
        # Before the first turn: a host that dies mid-turn must resume this thread, not start a second.
        try:
            self._save_thread(thread_id)
        except OSError as e:
            self._fail(f"could not keep the codex thread of this conversation: {e}")
            return
        self._thread_id = thread_id
        self._out.append({"method": THREAD_READY, "params": {
            "threadId": thread_id, "fresh": self._fresh,
            "model": result.get("model"), "modelProvider": result.get("modelProvider"),
        }})
        self._flush()
