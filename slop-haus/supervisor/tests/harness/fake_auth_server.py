"""A stand-in OAuth token endpoint that rotates refresh tokens and revokes the family on reuse, as providers do."""

from __future__ import annotations

import json
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any


class FakeAuthServer:
    def __init__(self) -> None:
        self._lock = threading.Lock()
        self._current = ""
        self._spent: set[str] = set()
        self._count = 0
        self.revoked = False
        self.rotations = 0
        self.rejections = 0
        self._http = ThreadingHTTPServer(("127.0.0.1", 0), self._handler())
        self._thread = threading.Thread(target=self._http.serve_forever, daemon=True)
        self._thread.start()
        self.initial_refresh_token = self._issue()

    @property
    def url(self) -> str:
        return f"http://127.0.0.1:{self._http.server_port}/oauth/token"

    def close(self) -> None:
        self._http.shutdown()
        self._http.server_close()
        self._thread.join()

    def _issue(self) -> str:
        self._count += 1
        if self._current:
            self._spent.add(self._current)
        self._current = f"refresh-{self._count}"
        return self._current

    def refresh(self, token: str) -> dict[str, Any] | None:
        """New tokens, or None when the token is refused. Presenting a spent token revokes the current one too."""
        with self._lock:
            if self.revoked or token != self._current:
                if token in self._spent:
                    self.revoked = True
                self.rejections += 1
                return None
            self.rotations += 1
            return {"access_token": f"access-{self._count + 1}", "id_token": f"id-{self._count + 1}", "refresh_token": self._issue()}

    def _handler(self) -> type[BaseHTTPRequestHandler]:
        server = self

        class Handler(BaseHTTPRequestHandler):
            def do_POST(self) -> None:
                body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
                answer = server.refresh(body.get("refresh_token", ""))
                payload = json.dumps(answer if answer is not None else {"error": "invalid_grant"}).encode()
                self.send_response(200 if answer is not None else 400)
                self.send_header("Content-Length", str(len(payload)))
                self.end_headers()
                self.wfile.write(payload)

            def log_message(self, format: str, *args: Any) -> None:
                pass

        return Handler
