from __future__ import annotations

from collections.abc import Callable
from typing import Any, Protocol

from slop_haus_supervisor.harness.text import as_dict, as_list
from slop_haus_supervisor.remote_control.host.activity import DONE, FAILED, RUNNING, fail_open_calls, call_id_of, claude_line, tool_name
from slop_haus_supervisor.remote_control.host.conversation import Conversation
from slop_haus_supervisor.remote_control.host.live_harness import LiveHarness
from slop_haus_supervisor.remote_control.host.records import TextReporter
from slop_haus_supervisor.text import clean_text
from slop_haus_supervisor.usage.conversation import as_int
from slop_haus_supervisor.usage.billing import claude_billing_mode


class TurnEnds(Protocol):
    def on_turn_result(self, conv: Conversation, record: dict[str, Any], add_usage: Callable[[], None]) -> None:
        """A turn ended with this result record: report it, then add its usage."""
        ...


class ClaudeRecords:
    # Claude Code can refuse a start-up because the host guessed wrongly whether the session exists.
    retries_session = True
    can_compact = True

    def __init__(self, reporter: TextReporter, turns: TurnEnds) -> None:
        self.reporter = reporter
        self.turns = turns

    def compact(self, conv: Conversation, live: LiveHarness) -> None:
        # Claude Code runs a `/compact` user message as the command, and ends it with a result.
        live.send("/compact")

    def explain(self, text: str) -> str:
        return text

    def describe_exit(self, stderr: str) -> str:
        return stderr

    def on_record(self, conv: Conversation, live: LiveHarness, record: dict[str, Any]) -> None:
        kind = record.get("type")
        if kind == "result" and not live.started:
            live.startup_result = record
        elif kind == "system" and record.get("subtype") == "init":
            live.started = True
            conv.state.created = True
            source = record.get("apiKeySource")
            conv.state.billing_mode = claude_billing_mode(source)
            # Saved now, not at the turn's end: a host that dies mid-turn must resume this
            # session, and would start a second one under the same id.
            conv.save()
        elif kind == "system" and record.get("subtype") == "status" and record.get("compact_result") == "failed":
            conv.compact_error = str(record.get("compact_error") or "the compaction failed")
        elif kind == "stream_event":
            self.on_stream_event(conv, record.get("event"))
        elif kind == "assistant":
            self.on_assistant(conv, record)
        elif kind == "user":
            self.on_tool_results(conv, record)
        elif kind == "result":
            fail_open_calls(conv, self.reporter)
            conv.state.turns += 1
            self.turns.on_turn_result(conv, record, lambda: conv.usage.add_turn(record))

    @staticmethod
    def meter(conv: Conversation, usage: dict[str, Any] | None) -> None:
        """The context is what the newest request read: a subagent's request is not the conversation's."""
        if usage is None:
            return
        tokens = sum(as_int(usage.get(k)) for k in ("input_tokens", "cache_read_input_tokens", "cache_creation_input_tokens"))
        if tokens > 0:
            conv.state.context_tokens = tokens

    def on_stream_event(self, conv: Conversation, event: Any) -> None:
        event = as_dict(event)
        if event is None:
            return
        if event.get("type") == "message_start":
            message = as_dict(event.get("message")) or {}
            conv.streaming_id = message["id"] if isinstance(message.get("id"), str) else None
        elif event.get("type") == "content_block_start":
            block = as_dict(event.get("content_block")) or {}
            if block.get("type") == "text" and conv.streaming_id is not None:
                conv.streamed.setdefault(conv.streaming_id, {})[event.get("index")] = ""
        elif event.get("type") == "content_block_delta":
            delta = as_dict(event.get("delta")) or {}
            if delta.get("type") == "text_delta" and delta.get("text"):
                text = str(delta["text"])
                if conv.streaming_id is not None:
                    blocks = conv.streamed.setdefault(conv.streaming_id, {})
                    blocks[event.get("index")] = blocks.get(event.get("index"), "") + text
                self.reporter.text(conv.state.id, clean_text(text), conv.streaming_id or "")

    def on_assistant(self, conv: Conversation, record: dict[str, Any]) -> None:
        message = as_dict(record.get("message")) or {}
        model = str(message.get("model") or "")
        if not record.get("parent_tool_use_id"):
            self.meter(conv, as_dict(message.get("usage")))
        # Claude Code writes one assistant record per content block, so a message's
        # text is matched to what was streamed by content, not by message id: a block
        # whose deltas never arrived (or only partly) must still reach the board.
        sent = conv.streamed.get(str(message.get("id")), {})
        for raw in as_list(message.get("content")):
            block = as_dict(raw)
            if block is None:
                continue
            if block.get("type") == "text" and block.get("text"):
                text = str(block["text"])
                # An exact match first, then the longest prefix: a short streamed block
                # must not claim the text of a longer one that starts the same way.
                matches = [i for i, got in sent.items() if got and text.startswith(got)]
                rest = text
                if matches:
                    best = max(matches, key=lambda i: (sent[i] == text, len(sent[i])))
                    rest = text[len(sent.pop(best)):]
                if rest:
                    self.reporter.text(conv.state.id, clean_text(rest), str(message.get("id") or ""))
            elif block.get("type") == "tool_use":
                tool = str(block.get("name") or "")
                conv.tool_uses[str(block.get("id"))] = (model, tool)
                conv.usage.add_call(model, tool)
                call = call_id_of(block.get("id"))
                name, line = conv.calls.start(call, tool_name(tool), claude_line(tool, block.get("input")))
                self.reporter.activity(conv.state.id, call, name, line, RUNNING)

    def on_tool_results(self, conv: Conversation, record: dict[str, Any]) -> None:
        message = as_dict(record.get("message")) or {}
        for raw in as_list(message.get("content")):
            block = as_dict(raw)
            if block is None or block.get("type") != "tool_result":
                continue
            failed = bool(block.get("is_error"))
            call = call_id_of(block.get("tool_use_id"))
            if call in conv.calls.calls:
                name, line = conv.calls.end(call)
                self.reporter.activity(conv.state.id, call, name, line, FAILED if failed else DONE)
            used = conv.tool_uses.get(str(block.get("tool_use_id")))
            if failed and used:
                conv.usage.add_error(*used)
