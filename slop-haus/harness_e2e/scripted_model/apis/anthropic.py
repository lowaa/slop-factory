"""The Anthropic Messages API (POST /v1/messages), streaming and not."""

from __future__ import annotations

import json
from typing import Any, Iterator

from ..model import ModelRequest
from ..scenario import Turn, Usage


def _text_of(blocks: Any) -> str:
    if isinstance(blocks, str):
        return blocks
    if isinstance(blocks, list):
        return "\n".join(b.get("text", "") for b in blocks if isinstance(b, dict) and b.get("type") == "text")
    return ""


def _flatten(messages: list[Any]) -> tuple[str, tuple[str, ...]]:
    """(all message text, tool-result text) of a Messages conversation."""
    texts: list[str] = []
    results: list[str] = []
    for message in messages:
        content = message.get("content") if isinstance(message, dict) else None
        if isinstance(content, str):
            texts.append(content)
            continue
        for block in content or []:
            if not isinstance(block, dict):
                continue
            if block.get("type") == "text":
                texts.append(block.get("text", ""))
            elif block.get("type") == "tool_result":
                text = _text_of(block.get("content", ""))
                results.append(text)
                texts.append(text)
    return "\n".join(texts), tuple(results)


def _sse(event: str, data: dict[str, Any]) -> bytes:
    return f"event: {event}\ndata: {json.dumps(data)}\n\n".encode()


def _usage_dict(usage: Usage) -> dict[str, int]:
    return {
        "input_tokens": usage.input_tokens,
        "output_tokens": usage.output_tokens,
        "cache_read_input_tokens": usage.cache_read_input_tokens,
        "cache_creation_input_tokens": usage.cache_creation_input_tokens,
    }


class AnthropicMessages:
    name = "anthropic"
    paths = ("/v1/messages",)

    def parse(self, body: dict[str, Any]) -> ModelRequest:
        message_text, tool_results = _flatten(body.get("messages", []))
        tools = tuple(t["name"] for t in body.get("tools", []) if isinstance(t, dict) and "name" in t)
        return ModelRequest(
            api=self.name,
            model=str(body.get("model", "")),
            system=_text_of(body.get("system", "")),
            tool_names=tools,
            messages=tuple(body.get("messages", [])),
            raw=body,
            message_text=message_text,
            tool_results=tool_results,
            # Claude Code makes tool-less calls for itself (quota probe, titles).
            auxiliary=not tools,
        )

    def content_type(self, request: ModelRequest) -> str:
        return "text/event-stream" if request.raw.get("stream") else "application/json"

    def encode(self, turn: Turn, request: ModelRequest, response_id: str) -> Iterator[bytes]:
        content: list[dict[str, Any]] = []
        if turn.text:
            content.append({"type": "text", "text": turn.text})
        for n, call in enumerate(turn.tool_calls):
            content.append({"type": "tool_use", "id": f"{response_id}_{n}", "name": call.name, "input": call.input})
        stop_reason = "tool_use" if turn.tool_calls else "end_turn"
        yield from self._frame(request, response_id, content, stop_reason, turn.usage)

    def encode_auxiliary(self, request: ModelRequest, response_id: str) -> Iterator[bytes]:
        yield from self._frame(request, response_id, [{"type": "text", "text": "ok"}], "end_turn", Usage())

    def encode_error(self, message: str) -> bytes:
        return json.dumps({"type": "error", "error": {"type": "invalid_request_error", "message": message}}).encode()

    def summarise(self, request: ModelRequest) -> str:
        last = request.messages[-1] if request.messages else None
        return (
            f"anthropic request: model={request.model!r} tools={len(request.tool_names)} "
            f"messages={len(request.messages)} last_message={json.dumps(last)[:600]}"
        )

    def _frame(
        self, request: ModelRequest, response_id: str, content: list[dict[str, Any]], stop_reason: str, usage: Usage
    ) -> Iterator[bytes]:
        message = {
            "id": response_id,
            "type": "message",
            "role": "assistant",
            "model": request.model,
            "stop_reason": stop_reason,
            "stop_sequence": None,
            "usage": _usage_dict(usage),
        }
        if not request.raw.get("stream"):
            yield json.dumps({**message, "content": content}).encode()
            return

        yield _sse("message_start", {"type": "message_start", "message": {**message, "content": [], "stop_reason": None}})
        for index, block in enumerate(content):
            if block["type"] == "text":
                start = {"type": "text", "text": ""}
                delta = {"type": "text_delta", "text": block["text"]}
            else:
                start = {**block, "input": {}}
                delta = {"type": "input_json_delta", "partial_json": json.dumps(block["input"])}
            yield _sse("content_block_start", {"type": "content_block_start", "index": index, "content_block": start})
            yield _sse("content_block_delta", {"type": "content_block_delta", "index": index, "delta": delta})
            yield _sse("content_block_stop", {"type": "content_block_stop", "index": index})
        yield _sse(
            "message_delta",
            {
                "type": "message_delta",
                "delta": {"stop_reason": stop_reason, "stop_sequence": None},
                "usage": _usage_dict(usage),
            },
        )
        yield _sse("message_stop", {"type": "message_stop"})
