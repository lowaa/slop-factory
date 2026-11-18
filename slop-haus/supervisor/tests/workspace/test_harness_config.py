from __future__ import annotations

import json
import logging
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path

from slop_haus_supervisor.slophaus.token import TokenUnavailable
from slop_haus_supervisor.workspace.harness_config import HarnessConfig, HarnessConfigError, HarnessConfigStore


def config(settings: dict[str, object], **extra: object) -> bytes:
    return json.dumps({"revision": 3, "settings": settings, **extra}).encode()


class HarnessConfigTest(unittest.TestCase):
    def test_claude_settings_become_variables_and_the_subagent_model_is_forced(self) -> None:
        parsed = HarnessConfig.parse(config({"claude": {"model": "opus", "subagent_model": "sonnet", "effort": "high"}}))
        assert parsed is not None
        self.assertEqual(
            {
                "ANTHROPIC_MODEL": "opus",
                "CLAUDE_CODE_SUBAGENT_MODEL": "sonnet",
                "CLAUDE_CODE_SUBAGENT_MODEL_FORCE": "1",
                "CLAUDE_CODE_EFFORT_LEVEL": "high",
            },
            parsed.env("claude"),
        )
        self.assertEqual({}, parsed.flags("claude"))
        self.assertEqual({}, parsed.env("pi"))

    def test_pi_settings_become_flags_and_never_variables(self) -> None:
        parsed = HarnessConfig.parse(config({"pi": {"provider": "openrouter", "model": "x/y", "thinking": "off"}, "claude": {"model": "opus"}}))
        assert parsed is not None
        self.assertEqual({"provider": "openrouter", "model": "x/y", "thinking": "off"}, parsed.flags("pi"))
        self.assertEqual({}, parsed.env("pi"))

    def test_a_value_that_fails_its_pattern_or_a_setting_it_does_not_know_is_left_out(self) -> None:
        parsed = HarnessConfig.parse(config({"claude": {"model": "--evil", "effort": "extreme", "LD_PRELOAD": "x", "subagent_model": "a\nb"}}))
        assert parsed is not None
        self.assertEqual({}, parsed.env("claude"))
        pi = HarnessConfig.parse(config({"pi": {"model": "-x", "provider": "Up", "thinking": 3}}))
        assert pi is not None
        self.assertEqual({}, pi.flags("pi"))

    def test_codex_model_and_effort_become_its_variant(self) -> None:
        parsed = HarnessConfig.parse(config({"codex": {"model": "gpt-5", "effort": "high"}, "claude": {"model": "opus"}}))
        assert parsed is not None
        self.assertEqual("model=gpt-5,effort=high", parsed.variant("codex"))
        self.assertEqual("model=opus", parsed.variant("claude"))

    def test_a_codex_slot_with_nothing_set_keeps_codex_defaults(self) -> None:
        parsed = HarnessConfig.parse(config({"claude": {"model": "opus"}}))
        assert parsed is not None
        self.assertEqual("", parsed.variant("codex"))

    def test_pi_settings_become_its_variant(self) -> None:
        parsed = HarnessConfig.parse(config({"pi": {"provider": "openrouter", "model": "x/y", "thinking": "off"}}))
        assert parsed is not None
        self.assertEqual("provider=openrouter,model=x/y,thinking=off", parsed.variant("pi"))

    def test_an_effort_codex_does_not_know_is_refused_as_a_setting_problem(self) -> None:
        parsed = HarnessConfig.parse(config({"codex": {"effort": "extreme"}}))
        assert parsed is not None
        with self.assertRaisesRegex(HarnessConfigError, "codex effort"):
            parsed.variant("codex")

    def test_a_pi_thinking_level_it_does_not_know_is_refused_as_a_setting_problem(self) -> None:
        parsed = HarnessConfig.parse(config({"pi": {"thinking": "galaxy"}}))
        assert parsed is not None
        with self.assertRaisesRegex(HarnessConfigError, "pi thinking"):
            parsed.variant("pi")

    def test_what_is_not_a_config_is_none(self) -> None:
        for raw in (b"<html>", b"[]", b'{"settings": {}}', b'{"revision": true, "settings": {}}', b'{"revision": 0, "settings": {}}', b'{"revision": 1, "settings": []}'):
            self.assertIsNone(HarnessConfig.parse(raw), raw)

    def test_only_a_harness_this_knows_is_a_variant(self) -> None:
        self.assertEqual("pi", HarnessConfig.parse(config({}, harness="pi")).harness)  # pyright: ignore[reportOptionalMemberAccess]
        self.assertEqual("", HarnessConfig.parse(config({}, harness="other")).harness)  # pyright: ignore[reportOptionalMemberAccess]


class Answer:
    status = 204
    body = b""


class StoreTest(unittest.TestCase):
    def setUp(self) -> None:
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.root = Path(tmp.name)
        (self.root / ".slop-haus").mkdir()
        self.answer = Answer()
        self.requests: list[tuple[str, str]] = []
        answer, requests = self.answer, self.requests

        class Handler(BaseHTTPRequestHandler):
            def do_GET(self) -> None:  # noqa: N802
                requests.append((self.path, self.headers.get("Authorization", "")))
                self.send_response(answer.status)
                if answer.status == 302:
                    self.send_header("Location", "/stolen")
                self.send_header("Content-Length", str(len(answer.body)))
                self.end_headers()
                self.wfile.write(answer.body)

            def log_message(self, format: str, *args: object) -> None:  # noqa: A002
                pass

        self.http = HTTPServer(("127.0.0.1", 0), Handler)
        threading.Thread(target=self.http.serve_forever, daemon=True).start()
        self.addCleanup(self.http.server_close)
        self.addCleanup(self.http.shutdown)
        self.token = "tok"
        self.logger = logging.getLogger("test-harness-config")

    def store(self, server: str | None = None, token: str | None = None) -> HarnessConfigStore:
        def get() -> str:
            if self.token == "":
                raise TokenUnavailable("no")
            return self.token

        server = server if server is not None else f"http://127.0.0.1:{self.http.server_address[1]}"
        return HarnessConfigStore(self.root, server, get, self.logger, timeout=2)

    @property
    def saved(self) -> Path:
        return self.root / ".slop-haus" / "harness-config.json"

    def test_a_config_is_fetched_with_the_token_in_a_header_and_saved(self) -> None:
        self.answer.status, self.answer.body = 200, config({"claude": {"model": "opus"}})
        store = self.store()
        store.refresh()
        self.assertEqual([("/api/workspace/harness_config", "Bearer tok")], self.requests)
        loaded = store.load()
        assert loaded is not None
        self.assertEqual(3, loaded.revision)

    def test_a_redirect_is_not_followed_with_the_token_and_the_saved_config_stands(self) -> None:
        self.saved.write_bytes(config({"claude": {"model": "old"}}))
        self.answer.status = 302
        with self.assertLogs(self.logger, "WARNING"):
            got = self.store().refresh()
        self.assertEqual([("/api/workspace/harness_config", "Bearer tok")], self.requests)
        assert got is not None
        self.assertEqual("old", got.settings["claude"]["model"])

    def test_refresh_returns_what_it_fetched_not_what_the_file_holds_by_then(self) -> None:
        self.answer.status, self.answer.body = 200, config({"claude": {"model": "opus"}})
        got = self.store().refresh()
        assert got is not None
        self.assertEqual("opus", got.settings["claude"]["model"])
        self.answer.status, self.answer.body = 204, b""
        self.assertIsNone(self.store().refresh())

    def test_nothing_configured_removes_the_saved_config(self) -> None:
        self.saved.write_bytes(config({}))
        self.answer.status = 204
        store = self.store()
        store.refresh()
        self.assertIsNone(store.load())

    def test_an_answer_that_is_not_a_config_is_never_saved_over_the_last_one(self) -> None:
        self.saved.write_bytes(config({"claude": {"model": "old"}}))
        self.answer.status, self.answer.body = 200, b"<html>proxy error</html>"
        with self.assertLogs(self.logger, "WARNING") as logs:
            self.store().refresh()
        self.assertIn("using the one saved last time", logs.output[0])
        self.assertEqual(config({"claude": {"model": "old"}}), self.saved.read_bytes())

    def test_a_server_that_errors_or_is_unreachable_warns_and_goes_on(self) -> None:
        self.answer.status = 409
        with self.assertLogs(self.logger, "WARNING") as logs:
            self.store().refresh()
        self.assertIn("answered 409", logs.output[0])
        self.assertIn("starting without one; run.env alone applies", logs.output[0])
        with self.assertLogs(self.logger, "WARNING") as logs:
            self.store(server="http://127.0.0.1:1").refresh()
        self.assertIn("could not be reached", logs.output[0])

    def test_no_token_warns_and_asks_the_server_nothing(self) -> None:
        self.token = ""
        with self.assertLogs(self.logger, "WARNING") as logs:
            self.store().refresh()
        self.assertIn("no server address or token", logs.output[0])
        self.assertEqual([], self.requests)

    def test_a_saved_file_that_is_not_a_config_stops_the_start(self) -> None:
        self.saved.write_text("nope")
        with self.assertRaisesRegex(HarnessConfigError, "is not a harness config"):
            self.store().load()

    def test_a_symlinked_saved_config_is_refused(self) -> None:
        target = self.root / "elsewhere.json"
        target.write_bytes(config({}))
        self.saved.symlink_to(target)
        with self.assertRaisesRegex(HarnessConfigError, "symlink"):
            self.store().load()
        self.answer.status, self.answer.body = 200, config({})
        with self.assertRaisesRegex(HarnessConfigError, "symlink"):
            self.store().refresh()


if __name__ == "__main__":
    unittest.main()
