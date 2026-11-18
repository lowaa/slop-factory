from __future__ import annotations

import json
import logging
import uuid
from collections.abc import Callable
from pathlib import Path
from typing import Any, Protocol

from slop_haus_supervisor.harness.text import as_dict, as_list
from slop_haus_supervisor.remote_control.host.activity import DONE, FAILED, RUNNING, fail_open_calls, base_name, call_id_of, tool_name
from slop_haus_supervisor.remote_control.host.conversation import Conversation
from slop_haus_supervisor.remote_control.host.live_harness import LiveHarness
from slop_haus_supervisor.remote_control.host.records import TextReporter
from slop_haus_supervisor.remote_control.pi.errors import PiErrors
from slop_haus_supervisor.text import clean_text, truncate_bytes
from slop_haus_supervisor.usage.billing import counter_change, pi_billing_mode, pi_counters
from slop_haus_supervisor.usage.conversation import MODEL, PI_PROVIDER

log = logging.getLogger(__name__)

DIALOGS = ("select", "confirm", "input", "editor")
EXPLAINED_BYTES = 1000


class TurnSink(Protocol):
    """What ends a turn, which the host owns."""

    def report_error_text(self, conv: Conversation, reason: str) -> None: ...
    def finish_turn(self, conv: Conversation, add_usage: Callable[[], None]) -> None: ...


class PiProcessState:
    """What PiRpc knows about one pi process: where its turn is."""

    def __init__(self) -> None:
        self.next_id = 0
        self.stats_id = ""
        # A text delta was seen for the message in flight, so its end must not report the same text again.
        self.streamed = False
        # The message in flight, which the board shows apart from the turn's others.
        self.block = ""
        self.provider = ""
        self.model = ""
        # What went wrong in the turn, shown once it has ended.
        self.error = ""

    def new_id(self, prefix: str) -> str:
        self.next_id += 1
        return f"{prefix}{self.next_id}"


class PiRpc:
    """pi's RPC protocol, and the one place in the host that knows it: what a record from
    `pi --mode rpc` means to a conversation.

    Taken from pi 0.99.1's own docs (rpc.md, rpc-commands.md, json.md, message-types.md) and its
    CLI, and run against a stand-in, never a model: a `prompt` command is answered at once and its
    turn ends at `agent_settled` (`agent_end` can be followed by a retry or a compaction); the
    turn's usage is `get_session_stats`, asked for at that point. Not verified: that a subscription
    login is the `"type": "oauth"` entry of auth.json (what billing_mode reads), and the shape of
    a turn that ends in a provider error beyond what the docs say (an assistant message with
    stopReason "error").
    """

    # pi's --session-id creates a session that is missing, so there is no wrong guess about it.
    retries_session = False
    can_compact = True

    def __init__(self, reporter: TextReporter, turns: TurnSink, errors: PiErrors, agent_dir: Path) -> None:
        self.reporter = reporter
        self.turns = turns
        self.errors = errors
        self.agent_dir = agent_dir
        # The host holds one harness at a time, and a new one has a new generation.
        self._state = PiProcessState()
        self._generation = 0

    def compact(self, conv: Conversation, live: LiveHarness) -> None:
        live.write(json.dumps({"id": self.state_of(live).new_id("compact-"), "type": "compact"}))

    def explain(self, text: str) -> str:
        return self.errors.explain(truncate_bytes(text, EXPLAINED_BYTES))

    def describe_exit(self, stderr: str) -> str:
        return self.errors.explain(self.errors.tidy(stderr)).strip()

    def state_of(self, live: LiveHarness) -> PiProcessState:
        if live.generation != self._generation:
            self._generation = live.generation
            self._state = PiProcessState()
        return self._state

    def on_record(self, conv: Conversation, live: LiveHarness, record: dict[str, Any]) -> None:
        state = self.state_of(live)
        kind = record.get("type")
        if kind == "response":
            self.on_response(conv, live, state, record)
        elif kind == "agent_settled":
            if conv.turn_active and not state.stats_id:
                state.stats_id = state.new_id("stats-")
                live.write(json.dumps({"id": state.stats_id, "type": "get_session_stats"}))
        elif kind == "message_start":
            state.streamed = False
            state.block = uuid.uuid4().hex[:16]
        elif kind == "message_update":
            event = as_dict(record.get("assistantMessageEvent")) or {}
            if event.get("type") == "text_delta" and isinstance(event.get("delta"), str):
                state.streamed = True
                self.reporter.text(conv.state.id, clean_text(event["delta"]), state.block)
        elif kind == "message_end":
            self.on_message_end(conv, state, record.get("message"))
        elif kind == "tool_execution_start":
            tool, call = str(record.get("toolName") or ""), str(record.get("toolCallId"))
            conv.tool_uses[call] = (state.model, tool)
            conv.usage.add_call(state.model, tool)
            name, line = conv.calls.start(call_id_of(call), tool_name(tool), self.line_of(tool, record.get("args")))
            self.reporter.activity(conv.state.id, call_id_of(call), name, line, RUNNING)
        elif kind == "tool_execution_end":
            call = call_id_of(record.get("toolCallId"))
            name, line = conv.calls.end(call, tool_name(record.get("toolName")))
            self.reporter.activity(conv.state.id, call, name, line, FAILED if record.get("isError") else DONE)
            used = conv.tool_uses.get(str(record.get("toolCallId")))
            if record.get("isError") and used:
                conv.usage.add_error(*used)
        elif kind == "auto_retry_end" and record.get("success") is False:
            state.error = str(record.get("finalError") or "the model request failed")
        elif kind == "extension_ui_request" and record.get("method") in DIALOGS:
            # Nobody is at a terminal to answer; left unanswered it would hold the turn for ever.
            live.write(json.dumps({"type": "extension_ui_response", "id": record.get("id"), "cancelled": True}))

    @staticmethod
    def line_of(tool: str, args: Any) -> str:
        """pi's file tools show the file's base name; its other tools have no line of the model's own,
        and their arguments (a command, a pattern) are never shown."""
        if tool in ("read", "edit", "write"):
            return base_name((as_dict(args) or {}).get("path"))
        return ""

    def on_message_end(self, conv: Conversation, state: PiProcessState, raw: Any) -> None:
        message = as_dict(raw)
        if message is None or message.get("role") != "assistant":
            return
        provider, model = message.get("provider"), message.get("model")
        if isinstance(provider, str) and isinstance(model, str) and PI_PROVIDER.match(provider) and MODEL.match(model):
            state.provider, state.model = provider, model
        if not state.streamed:
            for raw_block in as_list(message.get("content")):
                block = as_dict(raw_block)
                if block is not None and block.get("type") == "text" and block.get("text"):
                    self.reporter.text(conv.state.id, clean_text(str(block["text"])), state.block or uuid.uuid4().hex[:16])
        state.streamed = False
        if message.get("stopReason") == "error":
            state.error = str(message.get("errorMessage") or "the model request failed")

    def on_response(self, conv: Conversation, live: LiveHarness, state: PiProcessState, record: dict[str, Any]) -> None:
        if not conv.turn_active:
            return
        success = record.get("success") is True
        data = as_dict(record.get("data")) or {}
        if record.get("command") == "compact":
            self.on_compacted(conv, state, success, data, record)
            return
        # One prompt is in flight at a time (the host sends the next only after the turn ends),
        # so the command's name says which answer this is.
        if record.get("command") == "prompt":
            if not success:
                state.error = str(record.get("error") or "pi did not take the message")
                self.end_turn(conv, state, None)
            elif data.get("disposition") == "handled":
                # An extension command or an input handler took it: no run started, so no
                # agent_settled is coming.
                self.end_turn(conv, state, None)
        elif state.stats_id and record.get("id") == state.stats_id:
            state.stats_id = ""
            self.end_turn(conv, state, data if success else None)

    def on_compacted(self, conv: Conversation, state: PiProcessState, success: bool, data: dict[str, Any], record: dict[str, Any]) -> None:
        if not success:
            conv.compact_error = str(record.get("error") or "the compaction failed")
        else:
            # An estimate over the rebuilt context; the next turn's stats replace it.
            tokens = data.get("estimatedTokensAfter")
            if isinstance(tokens, int) and not isinstance(tokens, bool) and tokens > 0:
                conv.state.context_tokens = tokens
        conv.state.turns += 1
        self.turns.finish_turn(conv, lambda: None)

    def end_turn(self, conv: Conversation, state: PiProcessState, stats: dict[str, Any] | None) -> None:
        # null right after a compaction, until a response gives fresh usage: then the last figure stands.
        tokens = (as_dict((stats or {}).get("contextUsage")) or {}).get("tokens")
        if isinstance(tokens, int) and not isinstance(tokens, bool) and tokens > 0:
            conv.state.context_tokens = tokens
        fail_open_calls(conv, self.reporter)
        conv.state.turns += 1
        error, state.error = state.error, ""
        if error:
            self.turns.report_error_text(conv, error)
        self.turns.finish_turn(conv, lambda: self.add_usage(conv, state, stats))

    def add_usage(self, conv: Conversation, state: PiProcessState, stats: dict[str, Any] | None) -> None:
        """The turn's usage: the session's stats, less the stats the last turn ended on. The
        session's totals are for the whole session (every process that resumed it), so the change
        is what this turn spent, and what an interrupted one spent before it."""
        if stats is None:
            return
        now = pi_counters(stats)
        before = conv.state.pi_stats
        delta = counter_change(now, before)
        conv.state.pi_stats = now
        provider, model = state.provider or conv.state.pi_provider, state.model or conv.state.pi_model
        if not provider or not model:
            log.warning("pi usage of %s not reported: no model seen yet", conv.state.id)
            return
        conv.state.pi_provider, conv.state.pi_model = provider, model
        conv.usage.add_delta(provider, model, self.billing_mode(provider), delta)

    def billing_mode(self, provider: str) -> str:
        return pi_billing_mode(self.agent_dir, provider)
