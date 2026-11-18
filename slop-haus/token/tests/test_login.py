from __future__ import annotations

import io
import json
import os
import stat
import unittest
import urllib.parse
from typing import Dict, List, Optional, Sequence, Tuple

import slop_haus_token as tok
from slop_haus_token import Credentials, CredentialsError, DeviceLogin, Response
from test_refresh import FakeTransport, StoreCase

GRANT = Response(
    200,
    json.dumps(
        {
            "device_code": "dev-secret",
            "user_code": "ABCD-1234",
            "verification_uri_complete": "https://s.example/device?code=ABCD-1234",
            "expires_in": 600,
            "interval": 5,
        }
    ),
)
GOOD = Response(200, '{"access_token":"acc-secret","refresh_token":"ref-secret","expires_in":900}')
PENDING = Response(400, '{"error":"authorization_pending"}')


class DeviceLoginTest(unittest.TestCase):
    def setUp(self) -> None:
        self.said: List[str] = []
        self.sleeps: List[float] = []
        self.clock = 0.0

    def sign_in(self, transport: FakeTransport, hints: Optional[Dict[str, str]] = None) -> Credentials:
        def sleep(seconds: float) -> None:
            self.sleeps.append(seconds)
            self.clock += seconds

        flow = DeviceLogin("https://s.example", transport, self.said.append, sleep, lambda: self.clock, lambda: 1000.0)
        return flow.sign_in(hints or {})

    def fails(self, transport: FakeTransport) -> str:
        with self.assertRaises(CredentialsError) as caught:
            self.sign_in(transport)
        return str(caught.exception)

    def test_polls_until_approved_and_returns_the_tokens(self) -> None:
        creds = self.sign_in(FakeTransport(GRANT, PENDING, GOOD))
        self.assertEqual(Credentials("acc-secret", "ref-secret", 1900), creds)
        self.assertEqual([5, 5], self.sleeps)

    def test_hints_ride_on_the_url_a_person_opens_percent_encoded(self) -> None:
        self.sign_in(FakeTransport(GRANT, GOOD), {"kind": "supervisor", "agent": "o'brien & co"})
        url = next(word for word in self.said[0].split() if word.startswith("https://"))
        query = urllib.parse.parse_qs(urllib.parse.urlparse(url).query)
        self.assertEqual({"code": ["ABCD-1234"], "kind": ["supervisor"], "agent": ["o'brien & co"]}, query)
        self.assertIn("agent=o%27brien%20%26%20co", url)

    def test_slow_down_widens_the_interval_for_the_rest_of_the_run(self) -> None:
        self.sign_in(FakeTransport(GRANT, Response(400, '{"error":"slow_down"}'), PENDING, GOOD))
        self.assertEqual([5, 10, 10], self.sleeps)

    def test_device_code_goes_in_the_body_and_never_to_the_person(self) -> None:
        transport = FakeTransport(GRANT, GOOD)
        self.sign_in(transport)
        self.assertEqual("/oauth/device_authorization", transport.calls[0][0][len("https://s.example") :])
        self.assertIn(("device_code", "dev-secret"), transport.calls[1][1])
        self.assertNotIn("dev-secret", "".join(self.said))

    def test_each_refusal_ends_the_sign_in_with_a_reason(self) -> None:
        for error, words in (("access_denied", "denied"), ("expired_token", "token.sh login")):
            with self.subTest(error=error):
                self.assertIn(words, self.fails(FakeTransport(GRANT, Response(400, json.dumps({"error": error})))))

    def test_rerun_hint_is_the_callers(self) -> None:
        flow = DeviceLogin("https://s", FakeTransport(GRANT, Response(400, '{"error":"expired_token"}')), self.said.append, subject="supervisor", rerun="login")
        with self.assertRaisesRegex(CredentialsError, "run 'login' again"):
            flow.sign_in({})
        self.assertIn("this supervisor's access", self.said[0])

    def test_never_approved_times_out(self) -> None:
        self.assertIn("timed out", self.fails(FakeTransport(GRANT, PENDING)))

    def test_unreachable_server_at_either_step(self) -> None:
        self.assertIn("could not reach", self.fails(FakeTransport(Response(0))))
        self.assertIn("could not reach", self.fails(FakeTransport(GRANT, Response(0))))

    def test_server_that_refuses_to_start_says_why(self) -> None:
        refusal = Response(400, '{"error":"invalid_client","error_description":"unknown client"}')
        self.assertIn("unknown client", self.fails(FakeTransport(refusal)))

    def test_a_trailing_slash_in_the_server_does_not_double_up(self) -> None:
        transport = FakeTransport(GRANT, GOOD)
        flow = DeviceLogin("https://s.example/", transport, self.said.append, sleep=lambda _: None)
        flow.sign_in({})
        self.assertEqual(["https://s.example/oauth/device_authorization", "https://s.example/oauth/token"], [c[0] for c in transport.calls])

    def test_hostile_numbers_fall_back_to_defaults(self) -> None:
        hostile = Response(200, json.dumps({**json.loads(GRANT.body), "expires_in": "$(touch x)", "interval": -3}))
        self.sign_in(FakeTransport(hostile, GOOD))
        self.assertEqual([5], self.sleeps)
        self.assertIn("10 minute(s)", self.said[0])

    def test_a_zero_interval_does_not_busy_loop(self) -> None:
        zero = Response(200, json.dumps({**json.loads(GRANT.body), "interval": 0}))
        self.sign_in(FakeTransport(zero, GOOD))
        self.assertEqual([5], self.sleeps)

    def test_unrecognisable_responses_are_refused_without_echoing_tokens(self) -> None:
        self.assertIn("unrecognisable device authorization", self.fails(FakeTransport(Response(200, '{"device_code":"dev-secret"}'))))
        message = self.fails(FakeTransport(GRANT, Response(200, '{"access_token":"acc-secret"}')))
        self.assertIn("unrecognisable token", message)
        self.assertNotIn("acc-secret", message)

    def test_server_text_echoing_the_device_code_is_redacted(self) -> None:
        message = self.fails(FakeTransport(GRANT, Response(500, '{"error_description":"bad dev-secret"}')))
        self.assertNotIn("dev-secret", message)


class LoginMainTest(StoreCase):
    def login(self, transport: FakeTransport, *args: str, env: Optional[Dict[str, str]] = None) -> Tuple[int, str, str]:
        out, err = io.StringIO(), io.StringIO()
        environ = {"SLOP_HAUS_URL": "http://srv", **(env or {})}
        code = tok.main(["login", *args], self.tmp, environ, transport, lambda _s: None, out, err)
        return code, out.getvalue(), err.getvalue()

    def test_the_code_is_flushed_before_the_first_poll(self) -> None:
        class Tracked(io.StringIO):
            flushed = ""

            def flush(self) -> None:
                self.flushed = self.getvalue()

        out = Tracked()
        seen: List[str] = []
        tok.main(["login"], self.tmp, {"SLOP_HAUS_URL": "http://srv"}, FakeTransport(GRANT, GOOD), lambda _s: seen.append(out.flushed), out, io.StringIO())
        self.assertIn("ABCD-1234", seen[0])

    def stored(self) -> Credentials:
        return self.store.read()

    def test_stores_private_credentials_and_says_so(self) -> None:
        code, out, _ = self.login(FakeTransport(GRANT, GOOD), "my-agent")
        self.assertEqual(0, code)
        self.assertIn("agent=my-agent", out)
        self.assertIn("Authorized.", out)
        self.assertEqual(Credentials("acc-secret", "ref-secret", self.stored().expires_at), self.stored())
        self.assertEqual(0o600, stat.S_IMODE(os.stat(self.store.path).st_mode))

    def test_name_comes_from_the_argument_then_the_environment_then_the_recorded_file(self) -> None:
        with open(os.path.join(self.tmp, ".slop-haus.env"), "w", encoding="utf-8") as handle:
            handle.write("SLOP_HAUS_AGENT='o'\\''brien'\n")
        self.assertIn("agent=o%27brien", self.login(FakeTransport(GRANT, GOOD))[1])
        self.assertIn("agent=from-env", self.login(FakeTransport(GRANT, GOOD), env={"SLOP_HAUS_AGENT": "from-env"})[1])
        self.assertIn("agent=given", self.login(FakeTransport(GRANT, GOOD), "given", env={"SLOP_HAUS_AGENT": "from-env"})[1])

    def test_env_file_is_parsed_never_executed(self) -> None:
        with open(os.path.join(self.tmp, ".slop-haus.env"), "w", encoding="utf-8") as handle:
            handle.write("SLOP_HAUS_AGENT=$(touch pwned)\n")
        self.assertNotIn("agent=", self.login(FakeTransport(GRANT, GOOD))[1])
        self.assertEqual("", tok.recorded_agent_name(os.path.join(self.tmp, "absent")))

    def test_a_symlinked_env_file_is_ignored(self) -> None:
        real = os.path.join(self.tmp, "real.env")
        with open(real, "w", encoding="utf-8") as handle:
            handle.write("SLOP_HAUS_AGENT='planted'\n")
        os.symlink(real, os.path.join(self.tmp, ".slop-haus.env"))
        self.assertEqual("", tok.recorded_agent_name(self.tmp))

    def test_denied_stores_nothing(self) -> None:
        code, _, err = self.login(FakeTransport(GRANT, Response(400, '{"error":"access_denied"}')))
        self.assertEqual((1, True), (code, "denied" in err))
        self.assertFalse(os.path.exists(self.store.path))

    def test_login_replaces_an_existing_sign_in_and_clears_leftovers(self) -> None:
        self.seed()
        leftover = os.path.join(self.store.directory, "credentials.json.abc123")
        with open(leftover, "w", encoding="utf-8") as handle:
            handle.write("spent")
        self.login(FakeTransport(GRANT, GOOD))
        self.assertEqual("acc-secret", self.stored().access_token)
        self.assertFalse(os.path.exists(leftover))

    def test_login_waits_for_the_credentials_lock_only_after_approval(self) -> None:
        transport = FakeTransport(GRANT, GOOD)
        with self.store.locked():
            # The poll happens without the lock: a held lock only delays the final write.
            code, _, err = self.login(transport)
        self.assertEqual(2, len(transport.calls))
        self.assertEqual(1, code)
        self.assertIn("timed out waiting", err)
        self.assertFalse(os.path.exists(self.store.path))

    def test_no_secret_in_output_on_any_failure_path(self) -> None:
        cases: Dict[str, Sequence[Response]] = {
            "denied": (GRANT, Response(400, '{"error":"access_denied"}')),
            "echo": (GRANT, Response(500, "<html>dev-secret acc-secret</html>")),
            "unrecognisable": (GRANT, Response(200, '{"access_token":"acc-secret"}')),
        }
        for name, replies in cases.items():
            with self.subTest(name):
                _, out, err = self.login(FakeTransport(*replies))
                for secret in ("dev-secret", "acc-secret", "ref-secret"):
                    self.assertNotIn(secret, out + err)

    def test_server_url_is_written_when_asked(self) -> None:
        self.store.write_server_url("https://s.example/")
        path = os.path.join(self.store.directory, "server-url")
        with open(path, encoding="utf-8") as handle:
            self.assertEqual("https://s.example\n", handle.read())
        self.assertEqual(0o600, stat.S_IMODE(os.stat(path).st_mode))
