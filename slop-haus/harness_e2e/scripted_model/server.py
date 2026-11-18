"""HTTP front of the scripted model.

Harness-facing: each ApiAdapter's paths. Test-facing, under /_scripted/:
  PUT    /_scripted/scenario   load a scenario (resets the recording)
  GET    /_scripted/requests   every request received, in order
  GET    /_scripted/errors     the unexpected-request failures so far
  PUT    /_scripted/gates/<name>  release a turn that waits on <name>
  GET    /_scripted/remaining  how many scripted turns are unplayed
  DELETE /_scripted/scenario   reset
  GET    /healthz
"""

from __future__ import annotations

import itertools
import json
import logging
import os
from http import HTTPStatus
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any

from .apis import ApiAdapter
from .apis.anthropic import AnthropicMessages
from .apis.openai_chat import OpenAIChat
from .apis.openai_responses import OpenAIResponses
from .model import ModelRequest, ScriptedModel, UnexpectedRequest
from .scenario import ScenarioError, scenario_from_dict

CONTROL = "/_scripted"
# A conversation is large, but not this large: a bound so a bad client cannot
# have the server buffer without limit.
MAX_BODY = 64 * 1024 * 1024


def request_to_dict(request: ModelRequest) -> dict[str, Any]:
    return {
        "api": request.api,
        "model": request.model,
        "system": request.system,
        "tool_names": list(request.tool_names),
        "messages": list(request.messages),
        "message_text": request.message_text,
        "tool_results": list(request.tool_results),
        "auxiliary": request.auxiliary,
        "raw": request.raw,
    }


class ScriptedModelServer(ThreadingHTTPServer):
    daemon_threads = True

    def __init__(self, address: tuple[str, int], adapters: list[ApiAdapter], log: logging.Logger) -> None:
        super().__init__(address, _Handler)
        self.adapters = {path: adapter for adapter in adapters for path in adapter.paths}
        self.model = ScriptedModel(self._summarise)
        self.log = log
        self._ids = itertools.count(1)
        self.gate_timeout = float(os.environ.get("SCRIPTED_MODEL_GATE_TIMEOUT", "60"))

    def _summarise(self, request: ModelRequest) -> str:
        for adapter in self.adapters.values():
            if adapter.name == request.api:
                return adapter.summarise(request)
        return repr(request)

    def next_id(self) -> str:
        return f"msg_scripted_{next(self._ids)}"


class _Handler(BaseHTTPRequestHandler):
    server: ScriptedModelServer
    # Chunked responses and a streaming SSE body need 1.1.
    protocol_version = "HTTP/1.1"

    def log_message(self, format: str, *args: Any) -> None:
        self.server.log.info("%s %s", self.address_string(), format % args)

    def _read_json(self) -> Any:
        length = int(self.headers.get("Content-Length") or 0)
        if length > MAX_BODY:
            raise json.JSONDecodeError(f"body of {length} bytes is over the {MAX_BODY} limit", "", 0)
        return json.loads(self.rfile.read(length) or b"null")

    def _send_json(self, status: int, body: Any) -> None:
        data = json.dumps(body).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self) -> None:
        model = self.server.model
        if self.path == "/healthz":
            self._send_json(HTTPStatus.OK, {"ok": True})
        elif self.path == f"{CONTROL}/requests":
            self._send_json(HTTPStatus.OK, [request_to_dict(r) for r in model.recording.requests])
        elif self.path == f"{CONTROL}/errors":
            self._send_json(HTTPStatus.OK, model.recording.errors)
        elif self.path == f"{CONTROL}/remaining":
            self._send_json(HTTPStatus.OK, {"remaining": model.remaining()})
        else:
            self._send_json(HTTPStatus.NOT_FOUND, {"error": f"no such route {self.path}"})

    def do_PUT(self) -> None:
        if self.path.startswith(f"{CONTROL}/gates/"):
            self.server.model.gate(self.path.removeprefix(f"{CONTROL}/gates/")).set()
            return self._send_json(HTTPStatus.OK, {"ok": True})
        if self.path != f"{CONTROL}/scenario":
            return self._send_json(HTTPStatus.NOT_FOUND, {"error": f"no such route {self.path}"})
        try:
            self.server.model.load(scenario_from_dict(self._read_json()))
        except (ScenarioError, json.JSONDecodeError) as err:
            return self._send_json(HTTPStatus.BAD_REQUEST, {"error": str(err)})
        self._send_json(HTTPStatus.OK, {"ok": True})

    def do_DELETE(self) -> None:
        if self.path != f"{CONTROL}/scenario":
            return self._send_json(HTTPStatus.NOT_FOUND, {"error": f"no such route {self.path}"})
        self.server.model.reset()
        self._send_json(HTTPStatus.OK, {"ok": True})

    def do_POST(self) -> None:
        path = self.path.split("?", 1)[0]
        adapter = self.server.adapters.get(path)
        try:
            body = self._read_json()
        except json.JSONDecodeError as err:
            return self._send_json(HTTPStatus.BAD_REQUEST, {"error": f"bad JSON: {err}"})
        if adapter is None:
            # Fail loudly: an API we do not speak is a harness doing something
            # the scenario did not plan for.
            err = self.server.model.fail(f"unexpected request: POST {path} is not an API this server speaks")
            return self._send_json(HTTPStatus.NOT_FOUND, {"error": str(err)})
        request = adapter.parse(body)
        try:
            turn, gate = self.server.model.answer_gated(request)
        except UnexpectedRequest as err:
            self.server.log.error("%s", err)
            data = adapter.encode_error(str(err))
            self.send_response(HTTPStatus.INTERNAL_SERVER_ERROR)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)
            return
        if gate is not None and not gate.wait(self.server.gate_timeout):
            err = self.server.model.fail(f"gate {turn.gate!r} was not opened within {self.server.gate_timeout}s", request)
            return self._send_json(HTTPStatus.INTERNAL_SERVER_ERROR, json.loads(adapter.encode_error(str(err))))
        response_id = self.server.next_id()
        chunks = (
            adapter.encode_auxiliary(request, response_id)
            if turn is None
            else adapter.encode(turn, request, response_id)
        )
        self.send_response(HTTPStatus.OK)
        self.send_header("Content-Type", adapter.content_type(request))
        self.send_header("Cache-Control", "no-cache")
        self.send_header("Transfer-Encoding", "chunked")
        self.end_headers()
        for chunk in chunks:
            self.wfile.write(f"{len(chunk):X}\r\n".encode() + chunk + b"\r\n")
        self.wfile.write(b"0\r\n\r\n")
        self.wfile.flush()


def make_server(host: str, port: int) -> ScriptedModelServer:
    return ScriptedModelServer((host, port), [AnthropicMessages(), OpenAIChat(), OpenAIResponses()], logging.getLogger("scripted_model"))
