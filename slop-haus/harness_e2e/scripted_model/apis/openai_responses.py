"""The OpenAI Responses API (POST /v1/responses), which Codex speaks. Streamed as Codex reads it:
response.created, one response.output_item.done per item, response.completed with the usage."""

from __future__ import annotations

import json
from typing import Any, Iterator

from ..model import ModelRequest
from ..scenario import Turn, Usage


def _text_of(content: Any) -> str:
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        return "\n".join(p.get("text", "") for p in content if isinstance(p, dict) and isinstance(p.get("text"), str))
    return ""


def _tools(raw_tools: list[Any]) -> dict[str, str]:
    """Tool name -> the namespace it is offered in ("" for none). Codex offers an MCP server's tools as the
    children of a namespace tool, and the model calls one by its bare name plus that namespace."""
    found: dict[str, str] = {}
    for tool in raw_tools:
        if not isinstance(tool, dict):
            continue
        if tool.get("type") == "namespace":
            for child in tool.get("tools") or []:
                if isinstance(child, dict) and isinstance(child.get("name"), str):
                    found[child["name"]] = str(tool.get("name", ""))
        elif isinstance(tool.get("name"), str):
            found[tool["name"]] = ""
    return found


def _flatten(instructions: str, items: list[Any]) -> tuple[str, str, tuple[str, ...]]:
    """(system text, all conversation text, tool-result text)."""
    system = [instructions] if instructions else []
    texts: list[str] = []
    results: list[str] = []
    for item in items:
        if not isinstance(item, dict):
            continue
        kind = item.get("type", "message")
        if kind == "message":
            text = _text_of(item.get("content"))
            if item.get("role") in ("system", "developer"):
                system.append(text)
            else:
                texts.append(text)
        elif kind in ("function_call_output", "custom_tool_call_output"):
            output = item.get("output")
            text = output if isinstance(output, str) else _text_of(output) or json.dumps(output)
            texts.append(text)
            results.append(text)
        elif kind in ("function_call", "custom_tool_call"):
            texts.append(str(item.get("arguments", item.get("input", ""))))
    return "\n".join(system), "\n".join(t for t in texts if t), tuple(results)


def _sse(event: str, data: dict[str, Any]) -> bytes:
    return f"event: {event}\ndata: {json.dumps(data)}\n\n".encode()


def _usage_dict(usage: Usage) -> dict[str, Any]:
    # Cached tokens are counted inside input_tokens, as in chat completions; there is no cache-write counter.
    prompt = usage.input_tokens + usage.cache_read_input_tokens
    return {
        "input_tokens": prompt,
        "input_tokens_details": {"cached_tokens": usage.cache_read_input_tokens},
        "output_tokens": usage.output_tokens,
        "output_tokens_details": {"reasoning_tokens": 0},
        "total_tokens": prompt + usage.output_tokens,
    }


class OpenAIResponses:
    name = "responses"
    paths = ("/v1/responses",)

    def parse(self, body: dict[str, Any]) -> ModelRequest:
        items = body.get("input", [])
        if isinstance(items, str):
            items = [{"type": "message", "role": "user", "content": items}]
        system, message_text, tool_results = _flatten(str(body.get("instructions") or ""), items)
        tools = tuple(_tools(body.get("tools") or []))
        return ModelRequest(
            api=self.name,
            model=str(body.get("model", "")),
            system=system,
            tool_names=tools,
            messages=tuple(items),
            raw=body,
            message_text=message_text,
            tool_results=tool_results,
            auxiliary=not tools,
        )

    def content_type(self, request: ModelRequest) -> str:
        return "text/event-stream" if request.raw.get("stream") else "application/json"

    def encode(self, turn: Turn, request: ModelRequest, response_id: str) -> Iterator[bytes]:
        namespaces = _tools(request.raw.get("tools") or [])
        items: list[dict[str, Any]] = []
        if turn.text:
            items.append(
                {
                    "type": "message",
                    "id": f"{response_id}_msg",
                    "role": "assistant",
                    "status": "completed",
                    "content": [{"type": "output_text", "text": turn.text, "annotations": []}],
                }
            )
        for n, call in enumerate(turn.tool_calls):
            item: dict[str, Any] = {
                "type": "function_call",
                "id": f"{response_id}_fc{n}",
                "call_id": f"{response_id}_call{n}",
                "name": call.name,
                "arguments": json.dumps(call.input),
                "status": "completed",
            }
            if namespaces.get(call.name):
                item["namespace"] = namespaces[call.name]
            items.append(item)
        yield from self._frame(request, response_id, items, turn.usage)

    def encode_auxiliary(self, request: ModelRequest, response_id: str) -> Iterator[bytes]:
        message = {
            "type": "message",
            "id": f"{response_id}_msg",
            "role": "assistant",
            "status": "completed",
            "content": [{"type": "output_text", "text": "ok", "annotations": []}],
        }
        yield from self._frame(request, response_id, [message], Usage())

    def encode_error(self, message: str) -> bytes:
        return json.dumps({"error": {"type": "invalid_request_error", "message": message}}).encode()

    def summarise(self, request: ModelRequest) -> str:
        last = request.messages[-1] if request.messages else None
        return (
            f"responses request: model={request.model!r} tools={len(request.tool_names)} "
            f"items={len(request.messages)} last_item={json.dumps(last)[:600]}"
        )

    def _frame(self, request: ModelRequest, response_id: str, items: list[dict[str, Any]], usage: Usage) -> Iterator[bytes]:
        envelope = {"id": response_id, "object": "response", "model": request.model, "created_at": 0}
        done = {**envelope, "status": "completed", "output": items, "usage": _usage_dict(usage)}
        if not request.raw.get("stream"):
            yield json.dumps(done).encode()
            return
        yield _sse("response.created", {"type": "response.created", "response": {**envelope, "status": "in_progress", "output": []}})
        for index, item in enumerate(items):
            yield _sse("response.output_item.done", {"type": "response.output_item.done", "output_index": index, "item": item})
        # Codex reads the usage and the end of the turn from this event alone; the items were sent above.
        yield _sse("response.completed", {"type": "response.completed", "response": {**done, "output": []}})
