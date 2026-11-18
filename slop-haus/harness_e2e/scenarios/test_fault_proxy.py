"""The fault proxy's own tests, against a stand-in upstream: faults fire on the
requests they were armed for, in order, and everything else passes through."""

from __future__ import annotations

import http.client
import http.server
import threading
import unittest

from support.fault_proxy import BAD_GATEWAY_BODY, GARBAGE_BEARER, UNKNOWN_SESSION, Fault, FaultProxy


class Upstream:
    """Answers 200 with a JSON echo of the Authorization and Mcp-Session-Id it saw."""

    def __init__(self) -> None:
        self.seen: list[tuple[str, str, str, str, bytes]] = []
        self.release_stream = threading.Event()
        upstream = self

        class Handler(http.server.BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"

            def handle_any(self) -> None:
                length = int(self.headers.get("Content-Length") or 0)
                body = self.rfile.read(length)
                upstream.seen.append(
                    (self.command, self.path, self.headers.get("Authorization", ""), self.headers.get("Mcp-Session-Id", ""), body)
                )
                if self.path == "/stream":
                    self.send_response(200)
                    self.send_header("Content-Type", "text/event-stream")
                    self.send_header("Transfer-Encoding", "chunked")
                    self.end_headers()
                    self.wfile.write(b"6\r\nfirst\n\r\n")
                    self.wfile.flush()
                    upstream.release_stream.wait(10)
                    self.wfile.write(b"7\r\nsecond\n\r\n0\r\n\r\n")
                    return
                # What the API makes of the proxy's swapped-in credentials.
                status = 401 if self.headers.get("Authorization") == GARBAGE_BEARER else 404 if self.headers.get("Mcp-Session-Id") == UNKNOWN_SESSION else 200
                answer = b"ok"
                self.send_response(status)
                self.send_header("Content-Length", str(len(answer)))
                self.end_headers()
                self.wfile.write(answer)

            do_GET = do_POST = do_DELETE = handle_any

            def log_message(self, *args: object) -> None:
                pass

        self.httpd = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.httpd.daemon_threads = True
        self.url = f"http://127.0.0.1:{self.httpd.server_address[1]}"
        threading.Thread(target=self.httpd.serve_forever, daemon=True).start()

    def close(self) -> None:
        self.release_stream.set()
        self.httpd.shutdown()
        self.httpd.server_close()


class FaultProxyTest(unittest.TestCase):
    def setUp(self) -> None:
        self.upstream = Upstream()
        self.addCleanup(self.upstream.close)
        self.proxy = FaultProxy(self.upstream.url)
        self.addCleanup(self.proxy.close)

    def call(self, method: str, path: str, headers: dict[str, str] | None = None, body: bytes | None = None) -> tuple[int, bytes]:
        conn = http.client.HTTPConnection(self.proxy.url.removeprefix("http://"), timeout=10)
        try:
            conn.request(method, path, body=body, headers=headers or {})
            resp = conn.getresponse()
            return resp.status, resp.read()
        finally:
            conn.close()

    def test_unarmed_requests_pass_through_untouched(self) -> None:
        status, body = self.call("POST", "/mcp?x=1", {"Authorization": "Bearer real", "Mcp-Session-Id": "s1"}, b"payload")
        self.assertEqual((status, body), (200, b"ok"))
        self.assertEqual(self.upstream.seen, [("POST", "/mcp?x=1", "Bearer real", "s1", b"payload")])
        self.assertEqual(self.proxy.fired(), [])

    def test_unauthorized_swaps_in_a_bearer_the_upstream_does_not_know_once(self) -> None:
        self.proxy.arm("POST", "/mcp", Fault.UNAUTHORIZED)
        first = self.call("POST", "/mcp", {"Authorization": "Bearer real"}, b"{}")
        second = self.call("POST", "/mcp", {"Authorization": "Bearer real"}, b"{}")
        self.assertEqual([first[0], second[0]], [401, 200])
        self.assertEqual([s[2] for s in self.upstream.seen], [GARBAGE_BEARER, "Bearer real"])
        self.assertEqual([(e.fault, e.status) for e in self.proxy.events()], [(Fault.UNAUTHORIZED, 401), (None, 200)])

    def test_session_not_found_swaps_in_a_session_the_upstream_does_not_know(self) -> None:
        self.proxy.arm("POST", "/mcp", Fault.SESSION_NOT_FOUND)
        status, _ = self.call("POST", "/mcp", {"Authorization": "Bearer real", "Mcp-Session-Id": "s1"}, b"{}")
        self.assertEqual(status, 404)
        self.assertEqual([(s[2], s[3]) for s in self.upstream.seen], [("Bearer real", UNKNOWN_SESSION)])

    def test_bad_gateway_is_nginxs_and_never_reaches_the_upstream(self) -> None:
        self.proxy.arm("POST", "/oauth/token", Fault.BAD_GATEWAY)
        status, body = self.call("POST", "/oauth/token", body=b"grant_type=refresh_token")
        self.assertEqual((status, body), (502, BAD_GATEWAY_BODY))
        self.assertEqual(self.upstream.seen, [])

    def test_faults_apply_in_order_to_matching_requests_only(self) -> None:
        self.proxy.arm("POST", "/mcp", Fault.UNAUTHORIZED, Fault.BAD_GATEWAY)
        # Other methods, other paths and a request before the armed ones are not consumed.
        self.assertEqual(self.call("GET", "/mcp")[0], 200)
        self.assertEqual(self.call("POST", "/oauth/token", body=b"x")[0], 200)
        self.assertEqual(self.call("POST", "/mcp", body=b"{}")[0], 401)
        self.assertEqual(self.call("POST", "/mcp", body=b"{}")[0], 502)
        self.assertEqual(self.call("POST", "/mcp", body=b"{}")[0], 200)
        self.assertEqual([e.fault for e in self.proxy.events()], [None, None, Fault.UNAUTHORIZED, Fault.BAD_GATEWAY, None])
        self.assertEqual([e.fault for e in self.proxy.fired()], [Fault.UNAUTHORIZED, Fault.BAD_GATEWAY])

    def test_disarm_forgets_what_has_not_fired_and_says_how_much(self) -> None:
        self.proxy.arm("POST", "/mcp", Fault.UNAUTHORIZED, Fault.BAD_GATEWAY)
        self.call("POST", "/mcp", body=b"{}")
        self.assertEqual(self.proxy.disarm(), 1)
        self.assertEqual(self.call("POST", "/mcp", body=b"{}")[0], 200)
        self.assertEqual(self.proxy.disarm(), 0)

    def test_while_down_every_request_is_a_502_that_never_reaches_the_upstream_and_up_restores_it(self) -> None:
        self.proxy.arm("POST", "/mcp", Fault.UNAUTHORIZED)
        self.proxy.set_down(True)
        self.assertEqual([self.call("GET", "/a")[0], self.call("POST", "/b", body=b"x")[0]], [502, 502])
        self.assertEqual(self.upstream.seen, [])
        self.proxy.set_down(False)
        self.assertEqual(self.call("GET", "/a")[0], 200)
        # What was armed was not used up by the outage.
        self.assertEqual(self.call("POST", "/mcp", body=b"{}")[0], 401)

    def test_an_unreachable_upstream_is_a_502_not_a_hang(self) -> None:
        dead = FaultProxy("http://127.0.0.1:1")
        self.addCleanup(dead.close)
        conn = http.client.HTTPConnection(dead.url.removeprefix("http://"), timeout=10)
        conn.request("GET", "/")
        self.assertEqual(conn.getresponse().status, 502)
        conn.close()

    def test_a_streamed_answer_is_relayed_as_it_arrives(self) -> None:
        conn = http.client.HTTPConnection(self.proxy.url.removeprefix("http://"), timeout=10)
        self.addCleanup(conn.close)
        conn.request("GET", "/stream")
        resp = conn.getresponse()
        # The upstream is still holding its second event: the first reached us anyway.
        self.assertEqual(resp.readline(), b"first\n")
        self.upstream.release_stream.set()
        self.assertEqual(resp.read(), b"second\n")


if __name__ == "__main__":
    unittest.main()
