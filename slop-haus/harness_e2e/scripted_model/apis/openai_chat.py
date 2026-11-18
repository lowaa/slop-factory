"""The OpenAI chat completions API (POST /v1/chat/completions), streaming and not."""

from __future__ import annotations

import json
from typing import Any, Iterator

from ..model import ModelRequest
from ..scenario import Turn, Usage


def _text_of(content: Any) -> str:
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        return "\n".join(p.get("text", "") for p in content if isinstance(p, dict) and p.get("type") == "text")
    return ""


def _flatten(messages: list[Any]) -> tuple[str, str, tuple[str, ...]]:
    """(system text, all non-system message text, tool-result text)."""
    system: list[str] = []
    texts: list[str] = []
    results: list[str] = []
    for message in messages:
        if not isinstance(message, dict):
            continue
        text = _text_of(message.get("content"))
        role = message.get("role")
        if role in ("system", "developer"):
            system.append(text)
            continue
        texts.append(text)
        if role == "tool":
            results.append(text)
        for call in message.get("tool_calls") or []:
            texts.append(json.dumps(call.get("function", {}).get("arguments", "")))
    return "\n".join(system), "\n".join(t for t in texts if t), tuple(results)


def _sse(data: dict[str, Any] | str) -> bytes:
    return f"data: {data if isinstance(data, str) else json.dumps(data)}\n\n".encode()


def _usage_dict(usage: Usage) -> dict[str, Any]:
    # OpenAI counts cached tokens inside prompt_tokens; clients subtract them
    # again, so the scenario's input_tokens round-trips as the client's input.
    # There is no cache-write counter in the API, so cache_creation_input_tokens
    # has no representation and is ignored.
    prompt = usage.input_tokens + usage.cache_read_input_tokens
    return {
        "prompt_tokens": prompt,
        "completion_tokens": usage.output_tokens,
        "total_tokens": prompt + usage.output_tokens,
        "prompt_tokens_details": {"cached_tokens": usage.cache_read_input_tokens},
    }


class OpenAIChat:
    name = "openai"
    paths = ("/v1/chat/completions",)

    def parse(self, body: dict[str, Any]) -> ModelRequest:
        messages = body.get("messages", [])
        system, message_text, tool_results = _flatten(messages)
        tools = tuple(
            t["function"]["name"]
            for t in body.get("tools", [])
            if isinstance(t, dict) and isinstance(t.get("function"), dict) and "name" in t["function"]
        )
        return ModelRequest(
            api=self.name,
            model=str(body.get("model", "")),
            system=system,
            tool_names=tools,
            messages=tuple(messages),
            raw=body,
            message_text=message_text,
            tool_results=tool_results,
            auxiliary=not tools,
        )

    def content_type(self, request: ModelRequest) -> str:
        return "text/event-stream" if request.raw.get("stream") else "application/json"

    def encode(self, turn: Turn, request: ModelRequest, response_id: str) -> Iterator[bytes]:
        message: dict[str, Any] = {"role": "assistant", "content": turn.text or None}
        if turn.tool_calls:
            message["tool_calls"] = [
                {
                    "id": f"{response_id}_{n}",
                    "type": "function",
                    "function": {"name": call.name, "arguments": json.dumps(call.input)},
                }
                for n, call in enumerate(turn.tool_calls)
            ]
        yield from self._frame(request, response_id, message, "tool_calls" if turn.tool_calls else "stop", turn.usage)

    def encode_auxiliary(self, request: ModelRequest, response_id: str) -> Iterator[bytes]:
        yield from self._frame(request, response_id, {"role": "assistant", "content": "ok"}, "stop", Usage())

    def encode_error(self, message: str) -> bytes:
        return json.dumps({"error": {"type": "invalid_request_error", "message": message}}).encode()

    def summarise(self, request: ModelRequest) -> str:
        last = request.messages[-1] if request.messages else None
        return (
            f"openai request: model={request.model!r} tools={len(request.tool_names)} "
            f"messages={len(request.messages)} last_message={json.dumps(last)[:600]}"
        )

    def _frame(
        self, request: ModelRequest, response_id: str, message: dict[str, Any], finish_reason: str, usage: Usage
    ) -> Iterator[bytes]:
        envelope = {"id": response_id, "model": request.model, "created": 0}
        if not request.raw.get("stream"):
            yield json.dumps(
                {
                    **envelope,
                    "object": "chat.completion",
                    "choices": [{"index": 0, "message": message, "finish_reason": finish_reason}],
                    "usage": _usage_dict(usage),
                }
            ).encode()
            return

        def chunk(delta: dict[str, Any], finish: str | None = None, **extra: Any) -> bytes:
            return _sse(
                {**envelope, "object": "chat.completion.chunk", "choices": [{"index": 0, "delta": delta, "finish_reason": finish}], **extra}
            )

        yield chunk({"role": "assistant"})
        if message.get("content"):
            yield chunk({"content": message["content"]})
        for index, call in enumerate(message.get("tool_calls", [])):
            yield chunk({"tool_calls": [{"index": index, **call}]})
        yield chunk({}, finish_reason)
        # The usage-only final chunk of stream_options.include_usage, which
        # a client that did not ask for it ignores.
        yield _sse({**envelope, "object": "chat.completion.chunk", "choices": [], "usage": _usage_dict(usage)})
        yield _sse("[DONE]")
