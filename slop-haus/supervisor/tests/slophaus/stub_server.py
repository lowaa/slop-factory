from __future__ import annotations

import json
import threading
from collections.abc import Generator
from contextlib import contextmanager
from dataclasses import dataclass, field
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any


@dataclass
class Seen:
    method: str
    path: str
    auth: str
    body: str


@dataclass
class StubServer:
    """Answers each (method, path) with the next scripted (status, body); the last one repeats. A str body is sent as is."""

    script: dict[tuple[str, str], list[tuple[int, Any]]] = field(default_factory=lambda: {})
    seen: list[Seen] = field(default_factory=lambda: [])
    headers: dict[tuple[str, str], dict[str, str]] = field(default_factory=lambda: {})
    url: str = ""

    def header(self, method: str, path: str, name: str, value: str) -> None:
        self.headers.setdefault((method, path), {})[name] = value

    def on(self, method: str, path: str, *answers: tuple[int, Any]) -> None:
        self.script[(method, path)] = list(answers)

    def requests(self, method: str, path: str) -> list[Seen]:
        return [s for s in self.seen if (s.method, s.path) == (method, path)]

    def _answer(self, method: str, path: str) -> tuple[int, Any]:
        queue = self.script.get((method, path), [(404, {"detail": "no such route"})])
        return queue.pop(0) if len(queue) > 1 else queue[0]


@contextmanager
def serving(stub: StubServer) -> Generator[StubServer]:
    class Handler(BaseHTTPRequestHandler):
        def _handle(self) -> None:
            length = int(self.headers.get("Content-Length") or 0)
            body = self.rfile.read(length).decode() if length else ""
            stub.seen.append(Seen(self.command, self.path, self.headers.get("Authorization", ""), body))
            status, answer = stub._answer(self.command, self.path)  # pyright: ignore[reportPrivateUsage]
            payload = answer.encode() if isinstance(answer, str) else json.dumps(answer).encode()
            self.send_response(status)
            self.send_header("Content-Length", str(len(payload)))
            for name, value in stub.headers.get((self.command, self.path), {}).items():
                self.send_header(name, value)
            self.end_headers()
            self.wfile.write(payload)

        do_GET = do_POST = do_PUT = do_DELETE = _handle

        def log_message(self, format: str, *args: Any) -> None:
            pass

    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    stub.url = f"http://127.0.0.1:{server.server_port}"
    try:
        yield stub
    finally:
        server.shutdown()
        server.server_close()
