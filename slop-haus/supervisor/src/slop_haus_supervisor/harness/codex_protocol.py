"""Codex's `app-server` JSON-RPC protocol, and the one place that knows it."""

from __future__ import annotations

import json
import re
from collections.abc import Callable, Mapping
from typing import Any

from slop_haus_supervisor.harness import classify
from slop_haus_supervisor.harness.base import OutcomeKind, RunOutcome, Usage
from slop_haus_supervisor.harness.text import as_dict
from slop_haus_supervisor.ports import Clock
from slop_haus_supervisor.text import clean_text
from slop_haus_supervisor.usage.billing import SUBSCRIPTION
from slop_haus_supervisor.usage.codex import CODEX_PROVIDER, CodexUsage
from slop_haus_supervisor.usage.conversation import ConversationUsage

DETAIL_CHARS = 1500
# thread/resume's error for a thread with no rollout file. Any other error is a failure, never "missing", so a
# changed message fails loudly instead of discarding a conversation.
NO_ROLLOUT = "no rollout found for thread id"
UUID = re.compile(r"^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\Z", re.IGNORECASE)

INITIALIZE, THREAD, TURN = 1, 2, 3
METHOD_NOT_FOUND = -32601

# What a headless run answers a ServerRequest with. Nobody is at a terminal, so every ask is refused; unanswered,
# it would hold the turn for ever.
DECLINES: Mapping[str, dict[str, Any]] = {
    "item/commandExecution/requestApproval": {"decision": "decline"},
    "item/fileChange/requestApproval": {"decision": "decline"},
    "applyPatchApproval": {"decision": "denied"},
    "execCommandApproval": {"decision": "denied"},
    "mcpServer/elicitation/request": {"action": "decline"},
    "item/tool/requestUserInput": {"answers": {}},
    "item/permissions/requestApproval": {"permissions": {}, "scope": "turn"},
}


def is_uuid(value: str) -> bool:
    return UUID.match(value) is not None


class CodexProtocol:
    """initialize, thread/start or thread/resume, one turn/start; `turn/completed` ends the run.

    `thread` is the params of thread/start or thread/resume (resume when `resume_id` is set).
    """

    def __init__(self, prompt: str, clock: Clock, thread: Mapping[str, Any], resume_id: str = "", effort: str = "", billing_mode: str = SUBSCRIPTION) -> None:
        self._prompt = prompt
        self._clock = clock
        self._thread = dict(thread)
        self._resume_id = resume_id
        self._effort = effort
        self._billing_mode = billing_mode
        self._usage = CodexUsage()
        self._model = ""
        self.started = False
        self.session_missing = False
        self.thread_id = resume_id
        self._error = ""
        self.declined: list[str] = []

    def begin(self, write: Callable[[str], None]) -> None:
        write(json.dumps({"id": INITIALIZE, "method": "initialize", "params": {
            "clientInfo": {"name": "slop-haus-supervisor", "title": "slop-haus", "version": "1"},
            "capabilities": {},
        }}))

    def on_record(self, record: dict[str, Any], write: Callable[[str], None]) -> RunOutcome | None:
        method, rid = record.get("method"), record.get("id")
        if isinstance(method, str):
            if rid is not None:
                self._refuse(method, rid, write)
                return None
            return self._on_notification(method, as_dict(record.get("params")) or {})
        if "error" in record or "result" in record:
            return self._on_response(rid, record, write)
        return None

    def _refuse(self, method: str, rid: object, write: Callable[[str], None]) -> None:
        self.declined.append(method)
        decline = DECLINES.get(method)
        if decline is not None:
            write(json.dumps({"id": rid, "result": decline}))
        else:
            write(json.dumps({"id": rid, "error": {"code": METHOD_NOT_FOUND, "message": f"{method} is not supported by this client"}}))

    def _on_response(self, rid: object, record: dict[str, Any], write: Callable[[str], None]) -> RunOutcome | None:
        error = as_dict(record.get("error"))
        result = as_dict(record.get("result")) or {}
        if rid not in (INITIALIZE, THREAD, TURN):
            return None
        if error is not None:
            message = str(error.get("message") or "codex refused the request")
            if rid == THREAD and self._resume_id and NO_ROLLOUT in message:
                self.session_missing = True
                return RunOutcome(OutcomeKind.CRASHED, clean_text(message, DETAIL_CHARS))
            return self._failure(message)
        if rid == INITIALIZE:
            write(json.dumps({"method": "initialized"}))
            method = "thread/resume" if self._resume_id else "thread/start"
            params = {**self._thread, **({"threadId": self._resume_id} if self._resume_id else {})}
            write(json.dumps({"id": THREAD, "method": method, "params": params}))
        elif rid == THREAD:
            thread_id = (as_dict(result.get("thread")) or {}).get("id")
            if not isinstance(thread_id, str) or not is_uuid(thread_id):
                return self._failure("codex answered thread/start without a usable thread id")
            self.thread_id = thread_id
            if isinstance(result.get("model"), str):
                self._model = result["model"]
            self.started = True
            params = {"threadId": thread_id, "input": [{"type": "text", "text": self._prompt}]}
            if self._effort:
                params["effort"] = self._effort
            write(json.dumps({"id": TURN, "method": "turn/start", "params": params}))
        return None

    def _on_notification(self, method: str, params: dict[str, Any]) -> RunOutcome | None:
        if method == "error":
            error = as_dict(params.get("error")) or {}
            if params.get("willRetry") is not True and error.get("message"):
                self._error = str(error["message"])
            return None
        thread_id = params.get("threadId")
        if isinstance(thread_id, str) and self.thread_id and thread_id != self.thread_id:
            return None
        if method == "turn/started":
            self._usage.on_turn_started()
            return None
        if method == "thread/tokenUsage/updated":
            self._usage.on_notification(params)
            return None
        if method != "turn/completed":
            return None
        turn = as_dict(params.get("turn")) or {}
        status = turn.get("status")
        if status == "completed":
            return RunOutcome(OutcomeKind.FINISHED, usage=self._usage_of())
        message = str((as_dict(turn.get("error")) or {}).get("message") or self._error or f"the turn ended {status}")
        if status == "interrupted":
            return RunOutcome(OutcomeKind.CRASHED, clean_text(f"the turn was interrupted: {message}", DETAIL_CHARS), self._usage_of())
        return self._failure(message)

    def _usage_of(self) -> Usage:
        """What the turn spent, recorded under Codex's own provider whatever the config names; without a model the server could not price it, and a run with
        no usage notification has nothing to report."""
        counters = self._usage.counters()
        if not counters or not self._model:
            return Usage()
        usage = ConversationUsage()
        usage.add_delta(CODEX_PROVIDER, self._model, self._billing_mode, {**counters, "requests": 1})
        return usage.as_usage(self._billing_mode)

    def _failure(self, message: str) -> RunOutcome:
        detail = clean_text(message, DETAIL_CHARS)
        failure = classify.classify_text(message, self._clock.now())
        if failure is None:
            return RunOutcome(OutcomeKind.CRASHED, detail, self._usage_of())
        return RunOutcome(failure.kind, detail, self._usage_of(), failure.reset_at)

    def on_exit(self, code: int, stderr: str) -> RunOutcome:
        detail = clean_text(f"codex exited (status {code}) before the turn ended: {self._error or stderr}", DETAIL_CHARS)
        failure = classify.classify_text(f"{self._error}\n{stderr}", self._clock.now())
        if failure is not None:
            return RunOutcome(failure.kind, detail, self._usage_of(), failure.reset_at)
        return RunOutcome(OutcomeKind.CRASHED, detail, self._usage_of())
