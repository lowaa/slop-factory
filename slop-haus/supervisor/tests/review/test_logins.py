from __future__ import annotations

import json
import logging
import tempfile
import unittest
from collections.abc import Mapping
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

from slop_haus_supervisor.harness.codex_login import REFRESH_AFTER, CodexLogin
from slop_haus_supervisor.review.logins import CODEX_COPY, PROVIDERS, ReviewLogins, review_provider
from fakes import FakeClock

LOG = logging.getLogger("test")


def write_codex_login(home: Path, at: float, mode: str = "chatgpt") -> None:
    home.mkdir(parents=True, exist_ok=True)
    tokens = {"id_token": "id-0", "access_token": "access-0", "refresh_token": "master-refresh-0001"}
    auth = {"auth_mode": mode, "tokens": tokens, "last_refresh": datetime.fromtimestamp(at, timezone.utc).isoformat()}
    (home / "auth.json").write_text(json.dumps(auth))


def write_pi_login(home: Path, entry_type: str) -> None:
    home.mkdir(parents=True, exist_ok=True)
    (home / "auth.json").write_text(json.dumps({"openai": {"type": entry_type, "access": "pi-access-0002"}}))


class RecordingPost:
    """Answers the token endpoint with what the test gave it and records each request."""

    def __init__(self, answer: Mapping[str, Any]) -> None:
        self.answer = answer
        self.bodies: list[Mapping[str, str]] = []

    def __call__(self, url: str, body: Mapping[str, str]) -> Mapping[str, Any]:
        self.bodies.append(body)
        return self.answer


class ReviewLoginsTest(unittest.TestCase):
    def setUp(self) -> None:
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.root = Path(tmp.name)
        self.review_dir = self.root / "state" / "reviews" / "w1" / "tmp1"
        self.review_dir.mkdir(parents=True)
        self.clock = FakeClock()
        self.post = RecordingPost({"access_token": "access-1", "refresh_token": "rotated-refresh-0003"})
        self.refresher = CodexLogin(self.root / "harness" / "codex", self.clock, LOG, post=self.post)

    def logins(self, shared: bool = True) -> ReviewLogins:
        return ReviewLogins.under(self.root, self.refresher if shared else None)

    def test_the_provider_is_slop_reviews_default_unless_the_environment_names_one(self) -> None:
        self.assertEqual("claude", review_provider({}))
        self.assertEqual("claude", review_provider({"SLOP_REVIEW_PROVIDER": "  "}))
        self.assertEqual("codex", review_provider({"SLOP_REVIEW_PROVIDER": "codex"}))

    def test_every_provider_runs_with_every_login_directory_under_the_root(self) -> None:
        write_codex_login(self.root / "harness" / "codex", self.clock.now())
        for provider in PROVIDERS:
            hostile = {
                "SLOP_REVIEW_PROVIDER": provider,
                "CLAUDE_CONFIG_DIR": "/home/someone/.claude",
                "CODEX_HOME": "/home/someone/.codex",
                "PI_CODING_AGENT_DIR": "/home/someone/.pi/agent",
            }
            env = self.logins().scoped(hostile, self.review_dir)
            self.assertEqual(str(self.root / "harness" / "claude"), env["CLAUDE_CONFIG_DIR"], provider)
            self.assertEqual(str(self.review_dir / CODEX_COPY), env["CODEX_HOME"], provider)
            self.assertEqual(str(self.root / "harness" / "pi"), env["PI_CODING_AGENT_DIR"], provider)
            self.assertEqual(provider, env["SLOP_REVIEW_PROVIDER"])

    def test_codex_reviews_on_a_fresh_copy_and_never_on_the_master(self) -> None:
        master = self.root / "harness" / "codex"
        write_codex_login(master, self.clock.now())
        self.clock.advance(REFRESH_AFTER + 1)

        env = self.logins().scoped({"SLOP_REVIEW_PROVIDER": "codex", "OPENAI_API_KEY": "k", "CODEX_API_KEY": "k"}, self.review_dir)

        self.assertNotIn("OPENAI_API_KEY", env, "a key would override the ChatGPT login")
        self.assertNotIn("CODEX_API_KEY", env)
        copy = Path(env["CODEX_HOME"])
        self.assertNotEqual(master, copy)
        self.assertEqual(1, len(self.post.bodies), "a stale login is refreshed once, by the refresher, before the review")
        self.assertEqual("rotated-refresh-0003", json.loads((copy / "auth.json").read_text())["tokens"]["refresh_token"])
        self.assertEqual((master / "auth.json").read_text(), (copy / "auth.json").read_text())

    def test_another_provider_gets_no_codex_copy(self) -> None:
        write_codex_login(self.root / "harness" / "codex", self.clock.now())
        env = self.logins().scoped({"SLOP_REVIEW_PROVIDER": "claude"}, self.review_dir)
        self.assertFalse(Path(env["CODEX_HOME"]).exists(), "Codex, picked by the server's config instead, finds no login")
        self.assertEqual([], self.post.bodies, "nor is the login refreshed for it")

    def test_a_provider_with_its_scoped_login_may_review(self) -> None:
        write_codex_login(self.root / "harness" / "codex", self.clock.now())
        write_pi_login(self.root / "harness" / "pi", "oauth")
        cases: dict[str, dict[str, str]] = {
            "claude token": {"CLAUDE_CODE_OAUTH_TOKEN": "t"},
            "claude key": {"ANTHROPIC_API_KEY": "k"},
            "codex": {"SLOP_REVIEW_PROVIDER": "codex"},
            "chatgpt": {"SLOP_REVIEW_PROVIDER": "chatgpt"},
            "openrouter": {"SLOP_REVIEW_PROVIDER": "openrouter", "OPENROUTER_API_KEY": "k"},
            "openai": {"SLOP_REVIEW_PROVIDER": "openai", "OPENAI_API_KEY": "k"},
        }
        for name, env in cases.items():
            self.assertEqual("", self.logins().missing(env, "w1"), name)

    def test_a_provider_without_its_scoped_login_is_refused_with_the_fix(self) -> None:
        # A login in the wrong form is no login: an API-key Codex, a pi login to OpenAI by key.
        write_codex_login(self.root / "harness" / "codex", self.clock.now(), mode="apikey")
        write_pi_login(self.root / "harness" / "pi", "api_key")
        cases: dict[str, tuple[dict[str, str], str]] = {
            "claude": ({"CLAUDE_CODE_OAUTH_TOKEN": "", "OPENAI_API_KEY": "k"}, "`slop-haus harness login claude`"),
            "codex": ({"SLOP_REVIEW_PROVIDER": "codex"}, "`slop-haus harness login codex`"),
            "chatgpt": ({"SLOP_REVIEW_PROVIDER": "chatgpt"}, "`slop-haus harness login pi`"),
            "openrouter": ({"SLOP_REVIEW_PROVIDER": "openrouter", "OPENAI_API_KEY": "k"}, "`slop-haus secret set OPENROUTER_API_KEY`"),
            "openai": ({"SLOP_REVIEW_PROVIDER": "openai", "OPENROUTER_API_KEY": "k"}, "`slop-haus secret set OPENAI_API_KEY`"),
            "unknown": ({"SLOP_REVIEW_PROVIDER": "gemini"}, "not a provider the supervisor can check"),
        }
        for name, (env, fix) in cases.items():
            why = self.logins().missing(env, "w1")
            self.assertIn(fix, why, name)
            for secret in ("master-refresh-0001", "pi-access-0002"):
                self.assertNotIn(secret, why, name)

    def test_codex_without_the_shared_login_refresher_is_refused(self) -> None:
        write_codex_login(self.root / "harness" / "codex", self.clock.now())
        why = self.logins(shared=False).missing({"SLOP_REVIEW_PROVIDER": "codex"}, "w1")
        self.assertIn("SLOP_HAUS_CODEX_SHARED_LOGIN=verified", why)


if __name__ == "__main__":
    unittest.main()
