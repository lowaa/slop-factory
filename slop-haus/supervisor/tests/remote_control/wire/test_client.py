from __future__ import annotations

import logging
import threading
import time
import unittest
from collections.abc import Callable
from typing import Any

from slop_haus_supervisor.remote_control.wire.client import Client
from slop_haus_supervisor.remote_control.wire.errors import ServerError, Unreachable
from slop_haus_supervisor.slophaus.token import TokenUnavailable
from wire_fixtures import FAILSAFE, FakeTokens, StubServer


class ClientTest(unittest.TestCase):
    def serve(self, respond: Callable[[str, dict[str, Any], dict[str, str]], Any]) -> StubServer:
        server = StubServer(respond)
        self.addCleanup(server.close)
        return server

    def closed_server_url(self) -> str:
        server = StubServer(lambda _path, _body, _headers: {})
        url = server.url
        server.close()
        return url

    def test_sends_the_bearer_token_and_json(self) -> None:
        server = self.serve(lambda path, body, headers: {"ok": True})
        client = Client(server.url, FakeTokens())
        self.assertEqual({"ok": True}, client.post("/x", {"a": 1}, timeout=5))
        (request,) = server.requests
        self.assertEqual("Bearer token-1", request["headers"]["authorization"])
        self.assertEqual({"a": 1}, request["body"])

    def test_a_401_fetches_a_fresh_token_once(self) -> None:
        answers = iter([(401, {"detail": "expired"}), {"ok": True}])
        server = self.serve(lambda _path, _body, _headers: next(answers))
        tokens = FakeTokens()
        self.assertEqual({"ok": True}, Client(server.url, tokens).post("/x", {}, timeout=5))
        self.assertEqual(["", "token-1"], tokens.calls)
        self.assertEqual(["Bearer token-1", "Bearer token-2"], [r["headers"]["authorization"] for r in server.requests])

    def test_a_token_that_cannot_be_had_is_unreachable_and_hides_credentials(self) -> None:
        class NoTokens:
            def get(self, rejected: str = "") -> str:
                raise TokenUnavailable("token.sh gave no token: https://u:pw@h/")

        with self.assertRaises(Unreachable) as caught:
            Client("http://unused", NoTokens()).post("/x", {}, timeout=5)
        self.assertNotIn("pw", str(caught.exception))

    def test_a_second_401_is_an_error(self) -> None:
        server = self.serve(lambda _path, _body, _headers: (401, {"detail": "no"}))
        with self.assertRaises(ServerError) as caught:
            Client(server.url, FakeTokens()).post("/x", {}, timeout=5)
        self.assertEqual(401, caught.exception.status)
        self.assertEqual(2, len(server.requests))

    def test_a_redirect_is_never_followed_so_the_token_stays_here(self) -> None:
        elsewhere = self.serve(lambda _path, _body, _headers: {"ok": True})
        server = self.serve(lambda _path, _body, _headers: (302, {}, {"Location": elsewhere.url + "/stolen"}))
        with self.assertRaises(ServerError) as caught:
            Client(server.url, FakeTokens()).post("/x", {}, timeout=5)
        self.assertEqual(302, caught.exception.status)
        self.assertEqual([], elsewhere.requests)

    def test_errors_carry_the_servers_detail(self) -> None:
        server = self.serve(lambda _path, _body, _headers: (409, {"detail": "another host process is serving this conductor"}))
        with self.assertRaises(ServerError) as caught:
            Client(server.url, FakeTokens()).post("/x", {}, timeout=5)
        self.assertEqual(409, caught.exception.status)
        self.assertIn("another host", caught.exception.detail)

    def test_an_unreachable_server_is_its_own_error(self) -> None:
        with self.assertRaises(Unreachable):
            Client(self.closed_server_url(), FakeTokens()).post("/x", {}, timeout=2)

    def test_release_names_the_host_process(self) -> None:
        server = self.serve(lambda _path, _body, _headers: (204, {}))
        self.assertTrue(Client(server.url, FakeTokens()).release("host-1"))
        (request,) = server.requests
        self.assertEqual("/api/conductor/release", request["path"])
        self.assertEqual({"host_id": "host-1"}, request["body"])
        self.assertEqual("Bearer token-1", request["headers"]["authorization"])

    def test_a_release_that_fails_is_only_reported(self) -> None:
        server = self.serve(lambda _path, _body, _headers: (403, {"detail": "only a conductor's host may do that"}))
        with self.assertLogs(level=logging.WARNING) as logs:
            self.assertFalse(Client(server.url, FakeTokens()).release("host-1"))
        self.assertIn("only a conductor's host", logs.output[0])

    def test_an_unreachable_server_does_not_hold_up_a_release(self) -> None:
        with self.assertLogs(level=logging.WARNING):
            self.assertFalse(Client(self.closed_server_url(), FakeTokens()).release("host-1"))

    def test_a_server_that_never_answers_is_given_up_on_at_the_timeout(self) -> None:
        hold = threading.Event()
        self.addCleanup(hold.set)
        server = self.serve(lambda _path, _body, _headers: hold.wait(FAILSAFE) and {})
        with self.assertLogs(level=logging.WARNING) as logs:
            self.assertFalse(Client(server.url, FakeTokens()).release("host-1", timeout=0.2))
        self.assertIn("could not release", logs.output[0])

    def test_a_token_that_is_slow_to_come_does_not_hold_up_a_release(self) -> None:
        hold = threading.Event()
        self.addCleanup(hold.set)

        class SlowTokens:
            def get(self, rejected: str = "") -> str:
                hold.wait(FAILSAFE)
                return "t"

        server = self.serve(lambda _path, _body, _headers: {})
        with self.assertLogs(level=logging.WARNING) as logs:
            started = time.monotonic()
            self.assertFalse(Client(server.url, SlowTokens()).release("host-1", timeout=0.2))
        self.assertLess(time.monotonic() - started, FAILSAFE / 2)
        self.assertIn("could not release", logs.output[0])
        self.assertEqual([], server.requests)

    def test_the_401_retry_shares_the_release_timeout(self) -> None:
        hold = threading.Event()
        self.addCleanup(hold.set)

        class RetryTokens:
            def get(self, rejected: str = "") -> str:
                if rejected:
                    hold.wait(FAILSAFE)
                return "t"

        server = self.serve(lambda _path, _body, _headers: (401, {"detail": "expired"}))
        with self.assertLogs(level=logging.WARNING):
            started = time.monotonic()
            self.assertFalse(Client(server.url, RetryTokens()).release("host-1", timeout=0.2))
        self.assertLess(time.monotonic() - started, FAILSAFE / 2)
