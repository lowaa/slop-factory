from __future__ import annotations

import json
import sys
import threading
from collections.abc import Callable
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any

# Only keeps a hang from hanging the suite; nothing waits this long when it passes.
FAILSAFE = 30

CONV = "11111111-2222-4333-8444-555555555555"

Responder = Callable[[str, dict[str, Any], dict[str, str]], Any]


class QuietServer(ThreadingHTTPServer):
    """Tests that time out or abandon a request leave the handler writing to a closed socket;
    that is the scenario, not a failure, so it must not print a traceback."""

    def handle_error(self, request: Any, client_address: Any) -> None:
        if isinstance(sys.exc_info()[1], (BrokenPipeError, ConnectionResetError)):
            return
        super().handle_error(request, client_address)


class FakeTokens:
    def __init__(self) -> None:
        self.calls: list[str] = []
        self.n = 0

    def get(self, rejected: str = "") -> str:
        self.calls.append(rejected)
        self.n += 1
        return f"token-{self.n}"


class StubServer:
    """slop-haus as far as the host sees it: an HTTP server that records
    requests and answers each from a function the test supplies."""

    def __init__(self, respond: Responder) -> None:
        self.requests: list[dict[str, Any]] = []
        self.arrived = threading.Condition()
        outer = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, format: str, *args: Any) -> None:
                pass

            def do_GET(self) -> None:
                self.do_POST()

            def do_POST(self) -> None:
                body = json.loads(self.rfile.read(int(self.headers.get("content-length", 0) or 0)) or b"{}")
                headers = {k.lower(): v for k, v in self.headers.items()}
                with outer.arrived:
                    outer.requests.append({"path": self.path, "body": body, "headers": headers})
                    outer.arrived.notify_all()
                status, reply, extra = outer.normalise(respond(self.path, body, headers))
                data = json.dumps(reply).encode()
                self.send_response(status)
                for k, v in extra.items():
                    self.send_header(k, v)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)

        self.httpd = QuietServer(("127.0.0.1", 0), Handler)
        self.url = f"http://127.0.0.1:{self.httpd.server_address[1]}"
        self.thread = threading.Thread(target=self.httpd.serve_forever, kwargs={"poll_interval": 0.01}, daemon=True)
        self.thread.start()

    @staticmethod
    def normalise(reply: Any) -> tuple[int, Any, dict[str, str]]:
        if isinstance(reply, tuple):
            parts: tuple[Any, ...] = reply  # pyright: ignore[reportUnknownVariableType]
            return (parts[0], parts[1], parts[2] if len(parts) == 3 else {})
        return 200, reply, {}

    def close(self) -> None:
        self.httpd.shutdown()
        self.httpd.server_close()

    def wait_for(self, predicate: Callable[[list[dict[str, Any]]], bool]) -> None:
        with self.arrived:
            if not self.arrived.wait_for(lambda: predicate(self.requests), FAILSAFE):
                raise AssertionError(f"the stub server never saw what was waited for: {self.requests!r}")

    def to(self, path_part: str) -> list[dict[str, Any]]:
        with self.arrived:
            return [r for r in self.requests if path_part in r["path"]]
