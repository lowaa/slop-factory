from __future__ import annotations

import json
import threading
from collections.abc import Generator
from contextlib import contextmanager
from dataclasses import dataclass, field
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any

from openapi_spec import Spec, Tweak


@dataclass
class ContractServer:
    """Answers every request the way the spec says and records each way the request or the answer departs from it.

    A response is the sample of the operation's success schema, optionally reshaped by a `tweak` for a test that
    needs particular values; the tweaked response is validated too, so a test cannot stub in a shape the spec lacks.
    """

    spec: Spec
    tweaks: dict[tuple[str, str], Tweak] = field(default_factory=lambda: {})
    sent: list[tuple[str, str]] = field(default_factory=lambda: [])
    violations: list[str] = field(default_factory=lambda: [])
    url: str = ""
    _lock: threading.Lock = field(default_factory=threading.Lock)

    def tweak(self, method: str, template: str, change: Tweak) -> None:
        self.tweaks[(method, template)] = change

    def _violation(self, text: str) -> None:
        with self._lock:
            self.violations.append(text)

    def handle(self, method: str, path: str, raw: bytes) -> tuple[int, str, bytes]:
        with self._lock:
            self.sent.append((method, path.split("?", 1)[0]))
        operation = self.spec.find(method, path)
        if operation is None:
            self._violation(f"{method} {path}: the spec has no such route")
            return 404, "application/json", b'{"detail":"no such route"}'
        for error in self.spec.query_errors(operation, path):
            self._violation(f"{operation.label}: {error}")
        schema = operation.request_schema()
        if schema is None:
            # The client posts `{}` to routes that take no body; the server ignores it.
            if raw and raw != b"{}":
                self._violation(f"{operation.label}: sent a body the spec does not take")
        else:
            try:
                body = json.loads(raw)
            except ValueError:
                self._violation(f"{operation.label}: the request body is not JSON")
            else:
                for error in self.spec.errors(schema, body):
                    self._violation(f"{operation.label}: request body: {error}")
        status, content_type, response_schema = operation.success()
        if response_schema is None:
            return status, "", b""
        sample = self.spec.sample(response_schema)
        change = self.tweaks.get((method, operation.template))
        if change is not None:
            sample = change(sample)
        for error in self.spec.errors(response_schema, sample):
            self._violation(f"{operation.label}: response body: {error}")
        if content_type == "application/json":
            return status, content_type, json.dumps(sample).encode()
        return status, content_type, str(sample).encode()


@contextmanager
def serving(server: ContractServer) -> Generator[ContractServer]:
    class Handler(BaseHTTPRequestHandler):
        def _handle(self) -> None:
            length = int(self.headers.get("Content-Length") or 0)
            raw = self.rfile.read(length) if length else b""
            status, content_type, payload = server.handle(self.command, self.path, raw)
            self.send_response(status)
            if content_type:
                self.send_header("Content-Type", content_type)
            self.send_header("Content-Length", str(len(payload)))
            self.end_headers()
            self.wfile.write(payload)

        do_GET = do_POST = do_PUT = do_PATCH = do_DELETE = _handle

        def log_message(self, format: str, *args: Any) -> None:
            pass

    http = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    threading.Thread(target=http.serve_forever, daemon=True).start()
    server.url = f"http://127.0.0.1:{http.server_port}"
    try:
        yield server
    finally:
        http.shutdown()
        http.server_close()
