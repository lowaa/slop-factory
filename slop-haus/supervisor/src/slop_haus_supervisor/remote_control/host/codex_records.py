from __future__ import annotations

import logging
from collections.abc import Callable
from pathlib import Path
from typing import Any, Protocol

from slop_haus_supervisor.harness.codex_session import SESSION_ERROR, THREAD_READY, CodexSession
from slop_haus_supervisor.harness.text import as_dict, as_list
from slop_haus_supervisor.remote_control.host.activity import DONE, FAILED, RUNNING, fail_open_calls, base_name, call_id_of, tool_name
from slop_haus_supervisor.remote_control.host.conversation import Conversation
from slop_haus_supervisor.remote_control.host.live_harness import LiveHarness
from slop_haus_supervisor.remote_control.host.prompt import HOST_PREFIX
from slop_haus_supervisor.remote_control.host.records import TextReporter
from slop_haus_supervisor.text import clean_text
from slop_haus_supervisor.usage.billing import codex_login_billing_mode, counter_change
from slop_haus_supervisor.usage.codex import CODEX_PROVIDER, codex_counters, turn_baseline

log = logging.getLogger(__name__)

PROVIDER = CODEX_PROVIDER


class TurnSink(Protocol):
    """What ends a turn, which the host owns."""

    def report_error_text(self, conv: Conversation, reason: str) -> None: ...
    def finish_turn(self, conv: Conversation, add_usage: Callable[[], None]) -> None: ...


class CodexProcessState:
    """What CodexRecords knows about one app-server process."""

    def __init__(self) -> None:
        self.provider = PROVIDER
        self.model = ""
        # The thread's totals as the newest usage notification gave them.
        self.totals: dict[str, int] | None = None
        # Whether the current turn has started: Codex replays a resumed thread's standing before that.
        self.turn_started = False
        # What went wrong in the turn, shown once it has ended.
        self.error = ""


class CodexRecords:
    """What a record from `codex app-server` (as CodexSession passes them on) means to a conversation:
    an agent message is text, `turn/completed` ends the turn, a thread's token usage is the turn's
    spend. Records it does not know are ignored."""

    # thread/resume of a thread Codex lacks starts a new one (CodexSession), so there is no wrong guess to retry.
    retries_session = False
    can_compact = True

    def __init__(self, reporter: TextReporter, turns: TurnSink, config_dir: Path) -> None:
        self.reporter = reporter
        self.turns = turns
        self.config_dir = config_dir
        # The host holds one harness at a time, and a new one has a new generation.
        self._state = CodexProcessState()
        self._generation = 0

    def compact(self, conv: Conversation, live: LiveHarness) -> None:
        assert isinstance(live.session, CodexSession)
        live.session.compact()

    def explain(self, text: str) -> str:
        return text

    def describe_exit(self, stderr: str) -> str:
        return stderr

    def state_of(self, live: LiveHarness) -> CodexProcessState:
        if live.generation != self._generation:
            self._generation = live.generation
            self._state = CodexProcessState()
        return self._state

    def on_record(self, conv: Conversation, live: LiveHarness, record: dict[str, Any]) -> None:
        state = self.state_of(live)
        method = record.get("method")
        params = as_dict(record.get("params")) or {}
        if method == THREAD_READY:
            self.on_thread_ready(conv, state, params)
        elif method == SESSION_ERROR:
            self.on_session_error(conv, state, str(params.get("message") or "codex failed"))
        elif method == "turn/started":
            state.turn_started = True
        elif method == "item/started":
            self.on_item_started(conv, as_dict(params.get("item")))
        elif method == "item/completed":
            self.on_item(conv, state, as_dict(params.get("item")))
        elif method == "thread/tokenUsage/updated":
            self.on_token_usage(conv, state, as_dict(params.get("tokenUsage")))
        elif method == "error":
            error = as_dict(params.get("error")) or {}
            if params.get("willRetry") is not True and error.get("message"):
                state.error = str(error["message"])
        elif method == "turn/completed":
            self.on_turn_completed(conv, state, as_dict(params.get("turn")) or {})

    def on_thread_ready(self, conv: Conversation, state: CodexProcessState, params: dict[str, Any]) -> None:
        if isinstance(params.get("model"), str):
            state.model = params["model"]
        if isinstance(params.get("modelProvider"), str):
            state.provider = params["modelProvider"]
        conv.state.created = True
        # The thread is Codex's to name; a host that dies mid-turn must find it again.
        if params.get("fresh"):
            conv.state.codex_totals = {}
            self.reporter.text(
                conv.state.id,
                HOST_PREFIX + "this machine has no session for the conversation, so it continues in a new one without the earlier messages.",
            )
        conv.save()

    def on_session_error(self, conv: Conversation, state: CodexProcessState, message: str) -> None:
        """Codex refused a request: the owner is told, and a turn that was waiting on it is over."""
        if conv.turn_active:
            state.error = message
            self.end_turn(conv, state)
        else:
            self.turns.report_error_text(conv, message)

    @staticmethod
    def call_of(item: dict[str, Any]) -> tuple[str, str, str] | None:
        """A tool call item as (id, tool, line): the tool's name, and for a file change the base names of
        the files. Never the command, an MCP call's arguments or a result, which Codex puts in the same item."""
        kind = item.get("type")
        if kind == "commandExecution":
            tool, line = "shell", ""
        elif kind == "mcpToolCall":
            tool, line = tool_name(item.get("tool")), ""
        elif kind == "fileChange":
            changes = [as_dict(c) or {} for c in as_list(item.get("changes"))]
            names = [n for n in (base_name(c.get("path")) for c in changes[:3]) if n]
            tool, line = "edit", ", ".join(names)
        else:
            return None
        return call_id_of(item.get("id")), tool, line

    def on_item_started(self, conv: Conversation, item: dict[str, Any] | None) -> None:
        call = self.call_of(item) if item is not None else None
        if call:
            call_id, tool, line = call
            conv.calls.start(call_id, tool, line)
            self.reporter.activity(conv.state.id, call_id, tool, line, RUNNING)

    def on_item(self, conv: Conversation, state: CodexProcessState, item: dict[str, Any] | None) -> None:
        if item is None:
            return
        kind = item.get("type")
        call = self.call_of(item)
        if call:
            tool, line = conv.calls.end(call[0], call[1])
            self.reporter.activity(conv.state.id, call[0], tool, line or call[2], FAILED if item.get("status") == "failed" else DONE)
        if kind == "agentMessage" and item.get("text"):
            self.reporter.text(conv.state.id, clean_text(str(item["text"])), call_id_of(item.get("id")))
        elif kind in ("commandExecution", "mcpToolCall"):
            tool = "shell" if kind == "commandExecution" else str(item.get("tool") or "")
            conv.usage.add_call(state.model, tool)
            if item.get("status") == "failed":
                conv.usage.add_error(state.model, tool)

    def on_token_usage(self, conv: Conversation, state: CodexProcessState, usage: dict[str, Any] | None) -> None:
        total = as_dict((usage or {}).get("total"))
        if total is None:
            return
        state.totals = codex_counters(total)
        last = codex_counters(as_dict((usage or {}).get("last")) or {})
        # The newest request's input side; cached and written input are counted inside inputTokens.
        context = last["input"] + last["cache_read"] + last["cache_write"]
        if context > 0:
            conv.state.context_tokens = context
        if not conv.state.codex_totals:
            # A thread this host has not counted yet: what it had spent before this turn is not this host's.
            # After thread/resume Codex first replays the thread's standing (total == last), which is that
            # baseline; total - last is only right for a notification that is the turn's own.
            if state.turn_started:
                conv.state.codex_totals = turn_baseline(state.totals, codex_counters(as_dict((usage or {}).get("last")) or {}))
            else:
                conv.state.codex_totals = state.totals

    def on_turn_completed(self, conv: Conversation, state: CodexProcessState, turn: dict[str, Any]) -> None:
        if not conv.turn_active:
            return
        status = turn.get("status")
        # The owner's stop is reported by the host as "stopped by you", whatever status Codex gave the cancelled turn.
        if conv.interrupt_requested:
            pass
        elif status == "failed":
            state.error = str((as_dict(turn.get("error")) or {}).get("message") or state.error or "the turn failed")
        elif status != "completed":
            # Interrupted: not a failure, but the owner should see why the reply stops.
            state.error = state.error or f"the turn ended {status}"
        self.end_turn(conv, state)

    def end_turn(self, conv: Conversation, state: CodexProcessState) -> None:
        fail_open_calls(conv, self.reporter)
        conv.state.turns += 1
        state.turn_started = False
        error, state.error = state.error, ""
        if error and conv.compacting:
            # finish_turn tells the owner, and reports no divider.
            conv.compact_error = error
        elif error:
            self.turns.report_error_text(conv, error)
        self.turns.finish_turn(conv, lambda: self.add_usage(conv, state))

    def add_usage(self, conv: Conversation, state: CodexProcessState) -> None:
        """The turn's usage: the thread's totals, less the totals the last turn ended on. The totals are for
        the whole thread (every process that resumed it), so the change is what this turn spent, and what
        an interrupted one spent before it."""
        if state.totals is None:
            return
        delta = counter_change(state.totals, conv.state.codex_totals)
        delta["requests"] = 1
        conv.state.codex_totals = state.totals
        if not state.model:
            log.warning("codex usage of %s not reported: no model seen yet", conv.state.id)
            return
        conv.usage.add_delta(state.provider, state.model, codex_login_billing_mode(self.config_dir), delta)
