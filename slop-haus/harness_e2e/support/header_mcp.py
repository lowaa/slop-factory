"""A minimal streamable-HTTP MCP server that checks the Authorization header it is given, for a harness that
re-runs its header helper when a call is answered 401. It records every request, so a scenario sees which token each
call carried and how it was answered."""

from __future__ import annotations

import json
import threading
from dataclasses import dataclass
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any

TOOL = "whoami"
ANSWER = "you are the header mcp"


@dataclass(frozen=True)
class Seen:
    method: str
    authorization: str
    status: int


class HeaderMcp:
    """`expired` are the tokens a `tools/call` is refused (401) for; everything else is accepted."""

    def __init__(self, expired: set[str]) -> None:
        self.expired = expired
        self.seen: list[Seen] = []
        self._lock = threading.Lock()
        outer = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, format: str, *args: Any) -> None:
                pass

            def do_POST(self) -> None:
                message = json.loads(self.rfile.read(int(self.headers.get("Content-Length") or 0)) or b"null")
                auth = self.headers.get("Authorization", "")
                method = str(message.get("method", ""))
                if method == "tools/call" and auth.removeprefix("Bearer ") in outer.expired:
                    return self._reply(method, auth, 401, None)
                self._reply(method, auth, *outer.answer(message))

            def _reply(self, method: str, auth: str, status: int, body: dict[str, Any] | None) -> None:
                with outer._lock:
                    outer.seen.append(Seen(method, auth, status))
                data = json.dumps(body).encode() if body is not None else b""
                self.send_response(status)
                if body is not None:
                    self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)

        self._server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        threading.Thread(target=self._server.serve_forever, daemon=True).start()

    @property
    def url(self) -> str:
        return f"http://127.0.0.1:{self._server.server_address[1]}/mcp"

    @staticmethod
    def answer(message: dict[str, Any]) -> tuple[int, dict[str, Any] | None]:
        method, rid = message.get("method"), message.get("id")
        if rid is None:
            return 202, None
        if method == "initialize":
            result: dict[str, Any] = {
                "protocolVersion": message["params"]["protocolVersion"],
                "capabilities": {"tools": {}},
                "serverInfo": {"name": "header-mcp", "version": "0"},
            }
        elif method == "tools/list":
            result = {"tools": [{"name": TOOL, "description": "says who you are", "inputSchema": {"type": "object", "properties": {}}}]}
        elif method == "tools/call":
            result = {"content": [{"type": "text", "text": ANSWER}]}
        else:
            return 200, {"jsonrpc": "2.0", "id": rid, "error": {"code": -32601, "message": "not here"}}
        return 200, {"jsonrpc": "2.0", "id": rid, "result": result}

    def close(self) -> None:
        self._server.shutdown()
        self._server.server_close()
