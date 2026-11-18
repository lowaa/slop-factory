"""A reverse proxy in front of the slop-haus frontend that can be armed to fail
the next requests it sees, and that records what it did.

A fault is made as real as it can be: a 401 is the API's own, answering a
garbage bearer token; a 404 "session not found" is the API's own, answering an
unknown Mcp-Session-Id. Only a 502, which is what the frontend's nginx answers
while the API is down, has no real server to ask, so the proxy writes it in
nginx's shape. Python's standard library only, like scripted_model."""

from __future__ import annotations

import enum
import http.client
import http.server
import sys
import threading
import urllib.parse
from dataclasses import dataclass

HOP_BY_HOP = frozenset(
    {"connection", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailers", "transfer-encoding", "upgrade"}
)
GARBAGE_BEARER = "Bearer fault-proxy-not-a-token"
UNKNOWN_SESSION = "fault-proxy-unknown-session"
# What the frontend's nginx answers while the API behind it is down.
BAD_GATEWAY_BODY = (
    b"<html>\r\n<head><title>502 Bad Gateway</title></head>\r\n<body>\r\n"
    b"<center><h1>502 Bad Gateway</h1></center>\r\n<hr><center>nginx</center>\r\n</body>\r\n</html>\r\n"
)


class Fault(enum.Enum):
    # Forwarded with a bearer token the API does not know: its genuine 401.
    UNAUTHORIZED = "401"
    # Forwarded with a session id the API does not know: its genuine 404.
    SESSION_NOT_FOUND = "404 session not found"
    # Never forwarded.
    BAD_GATEWAY = "502"


@dataclass(frozen=True)
class Event:
    method: str
    path: str
    # None when the request was passed through untouched.
    fault: Fault | None
    status: int


class Rule:
    """The faults to apply, in order, to the next requests matching method and path."""

    def __init__(self, method: str, path: str, faults: list[Fault]) -> None:
        self.method = method
        self.path = path
        self.faults = list(faults)

    def matches(self, method: str, path: str) -> bool:
        return self.method == method and self.path == path


class QuietServer(http.server.ThreadingHTTPServer):
    daemon_threads = True

    def handle_error(self, request: object, client_address: object) -> None:
        # A client that hangs up mid-request (the harness being killed) is not news.
        if not isinstance(sys.exc_info()[1], ConnectionError):
            super().handle_error(request, client_address)  # type: ignore[arg-type]


class FaultProxy:
    def __init__(self, upstream: str) -> None:
        self._upstream = urllib.parse.urlsplit(upstream)
        self._lock = threading.Lock()
        self._rules: list[Rule] = []
        self._down = False
        self._events: list[Event] = []
        proxy = self

        class Handler(http.server.BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"

            def do_GET(self) -> None:  # noqa: N802
                proxy._handle(self)

            do_POST = do_PUT = do_DELETE = do_PATCH = do_HEAD = do_OPTIONS = do_GET

            def log_message(self, *args: object) -> None:
                pass

        self._httpd = QuietServer(("127.0.0.1", 0), Handler)
        self.url = f"http://127.0.0.1:{self._httpd.server_address[1]}"
        threading.Thread(target=self._httpd.serve_forever, daemon=True).start()

    def close(self) -> None:
        self._httpd.shutdown()
        self._httpd.server_close()

    # -- the test's side --

    def arm(self, method: str, path: str, *faults: Fault) -> None:
        """The next len(faults) requests for method and path (the query ignored) get
        these faults in order; the ones after them pass through."""
        with self._lock:
            self._rules.append(Rule(method, path, list(faults)))

    def set_down(self, down: bool) -> None:
        """While down, every request gets the 502 the frontend's nginx answers when the API is
        restarting, whatever is armed. Nothing reaches the upstream."""
        with self._lock:
            self._down = down

    def disarm(self) -> int:
        """Forgets the faults that have not fired; how many there were."""
        with self._lock:
            left = sum(len(r.faults) for r in self._rules)
            self._rules.clear()
            return left

    def events(self) -> list[Event]:
        with self._lock:
            return list(self._events)

    def fired(self) -> list[Event]:
        return [e for e in self.events() if e.fault is not None]

    def summary(self) -> str:
        lines = [f"{e.method} {e.path} -> {e.status}" + (f" (fault: {e.fault.value})" if e.fault else "") for e in self.events()]
        return "\n".join(lines[-40:]) or "no requests"

    # -- the proxy --

    def _take_fault(self, method: str, path: str) -> Fault | None:
        with self._lock:
            if self._down:
                return Fault.BAD_GATEWAY
            for rule in self._rules:
                if rule.faults and rule.matches(method, path):
                    return rule.faults.pop(0)
        return None

    def _record(self, method: str, path: str, fault: Fault | None, status: int) -> None:
        with self._lock:
            self._events.append(Event(method, path, fault, status))

    def _handle(self, req: http.server.BaseHTTPRequestHandler) -> None:
        path = urllib.parse.urlsplit(req.path).path
        body = self._read_body(req)
        fault = self._take_fault(req.command, path)
        if fault is Fault.BAD_GATEWAY:
            self._record(req.command, path, fault, 502)
            self._send_bad_gateway(req)
            return

        headers = {k: v for k, v in req.headers.items() if k.lower() not in HOP_BY_HOP | {"host", "content-length"}}
        headers["Host"] = self._upstream.netloc
        if fault is Fault.UNAUTHORIZED:
            headers = {k: v for k, v in headers.items() if k.lower() != "authorization"} | {"Authorization": GARBAGE_BEARER}
        elif fault is Fault.SESSION_NOT_FOUND:
            headers = {k: v for k, v in headers.items() if k.lower() != "mcp-session-id"} | {"Mcp-Session-Id": UNKNOWN_SESSION}
        if body is not None:
            headers["Content-Length"] = str(len(body))

        connection_class = http.client.HTTPSConnection if self._upstream.scheme == "https" else http.client.HTTPConnection
        upstream = connection_class(self._upstream.netloc, timeout=120)
        try:
            upstream.request(req.command, req.path, body=body, headers=headers)
            resp = upstream.getresponse()
        except OSError:
            self._record(req.command, path, None, 502)
            self._send_bad_gateway(req)
            upstream.close()
            return
        try:
            self._record(req.command, path, fault, resp.status)
            self._relay(req, resp)
        finally:
            upstream.close()

    @staticmethod
    def _read_body(req: http.server.BaseHTTPRequestHandler) -> bytes | None:
        if "chunked" in req.headers.get("Transfer-Encoding", "").lower():
            chunks = []
            while True:
                size = int(req.rfile.readline().split(b";")[0].strip() or b"0", 16)
                if size == 0:
                    while req.rfile.readline().strip():
                        pass
                    return b"".join(chunks)
                chunks.append(req.rfile.read(size))
                req.rfile.readline()
        length = req.headers.get("Content-Length")
        return req.rfile.read(int(length)) if length else None

    @staticmethod
    def _send_bad_gateway(req: http.server.BaseHTTPRequestHandler) -> None:
        req.send_response(502)
        req.send_header("Server", "nginx")
        req.send_header("Content-Type", "text/html")
        req.send_header("Content-Length", str(len(BAD_GATEWAY_BODY)))
        req.end_headers()
        req.wfile.write(BAD_GATEWAY_BODY)

    @staticmethod
    def _relay(req: http.server.BaseHTTPRequestHandler, resp: http.client.HTTPResponse) -> None:
        """The upstream's answer, passed on as it arrives: an MCP reply may be an event stream."""
        req.send_response(resp.status)
        bodiless = req.command == "HEAD" or resp.status in (204, 304)
        known_length = resp.getheader("Content-Length") is not None
        for key, value in resp.getheaders():
            if key.lower() not in HOP_BY_HOP:
                req.send_header(key, value)
        if not known_length and not bodiless:
            req.send_header("Transfer-Encoding", "chunked")
        req.end_headers()
        if bodiless:
            return
        while chunk := resp.read1(65536):
            if known_length:
                req.wfile.write(chunk)
            else:
                req.wfile.write(f"{len(chunk):x}\r\n".encode() + chunk + b"\r\n")
            req.wfile.flush()
        if not known_length:
            req.wfile.write(b"0\r\n\r\n")
            req.wfile.flush()
