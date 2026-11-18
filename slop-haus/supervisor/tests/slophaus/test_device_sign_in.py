from __future__ import annotations

import unittest
import urllib.parse

from slop_haus_supervisor.slophaus.credentials import TokenPair, device_sign_in
from slop_haus_supervisor.workspace.config import ConfigError
from stub_server import StubServer, serving

START = "/oauth/device_authorization"
TOKEN = "/oauth/token"
GRANT = {
    "device_code": "dev-secret",
    "user_code": "ABCD-1234",
    "verification_uri_complete": "https://s.example/device?code=ABCD-1234",
    "expires_in": 600,
    "interval": 5,
}
GOOD = {"access_token": "acc-secret", "refresh_token": "ref-secret", "expires_in": 900}


class DeviceSignInTest(unittest.TestCase):
    """The flow itself is tested with the module (token/tests); this covers the supervisor's wiring of it."""

    def setUp(self) -> None:
        self.stub = StubServer()
        cm = serving(self.stub)
        cm.__enter__()
        self.addCleanup(cm.__exit__, None, None, None)
        self.stub.on("POST", START, (200, GRANT))
        self.said: list[str] = []

    def sign_in(self, hints: dict[str, str] | None = None) -> TokenPair:
        return device_sign_in(self.stub.url, hints or {}, self.said.append, sleep=lambda _: None)

    def test_returns_the_tokens_and_sends_the_hints_on_the_url(self) -> None:
        self.stub.on("POST", TOKEN, (400, {"error": "authorization_pending"}), (200, GOOD))

        tokens = self.sign_in({"kind": "supervisor", "slots": "4"})

        self.assertEqual(("acc-secret", "ref-secret"), (tokens.access_token, tokens.refresh_token))
        url = next(w for w in self.said[0].split() if w.startswith("https://"))
        self.assertEqual({"code": ["ABCD-1234"], "kind": ["supervisor"], "slots": ["4"]}, urllib.parse.parse_qs(urllib.parse.urlparse(url).query))
        self.assertIn("this supervisor's access", self.said[0])
        self.assertIn("device_code=dev-secret", self.stub.requests("POST", TOKEN)[0].body)

    def test_a_refusal_is_a_config_error_with_the_supervisor_rerun_hint(self) -> None:
        self.stub.on("POST", TOKEN, (400, {"error": "expired_token"}))

        with self.assertRaisesRegex(ConfigError, "run 'login' again"):
            self.sign_in()
