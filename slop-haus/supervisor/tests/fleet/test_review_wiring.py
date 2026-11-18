from __future__ import annotations

import logging
import tempfile
import unittest
from pathlib import Path

from slop_haus_supervisor.fleet import wiring
from slop_haus_supervisor.ports import SubprocessRunner
from slop_haus_supervisor.review.logins import ReviewLogins
from slop_haus_supervisor.review.reviewer import ReviewSettings, review_environment
from slop_haus_supervisor.workspace.config import SupervisorConfig
from slop_haus_supervisor.workspace.layout import Layout


class ReviewWiringTest(unittest.TestCase):
    def setUp(self) -> None:
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.layout = Layout(Path(tmp.name))
        self.layout.secrets_file.write_text("CLAUDE_CODE_OAUTH_TOKEN=base-login-0001\n")
        self.layout.repo_env_file.write_text("OPENROUTER_API_KEY=repo-key-0002\n")
        self.layout.agent_dir("w1").mkdir(parents=True)
        (self.layout.agent_dir("w1") / "secrets.env").write_text("W1_SECRET=w1-secret-0003\nCLAUDE_CODE_OAUTH_TOKEN=w1-login-0004\n")

    def test_a_review_runs_with_the_agents_logins_and_repo_env_but_none_of_slop_hauss_own_names(self) -> None:
        environ = {
            "PATH": "/bin", "SLOP_HAUS_REVIEW_SCRIPT": "/r/review.sh", "SLOP_HAUS_TOKEN_IMPL": "bash", "SLOP_HAUS_LABELS": "gpu",
            "SLOP_HAUS_URL": "https://s", "SLOP_HAUS_AGENT": "someone", "CLAUDE_CODE_OAUTH_TOKEN": "inherited-0005",
        }

        env = wiring.review_env(self.layout, environ)("w1")

        self.assertEqual("w1-login-0004", env["CLAUDE_CODE_OAUTH_TOKEN"], "the agent's layer wins, as in its own runs")
        self.assertEqual("w1-secret-0003", env["W1_SECRET"])
        self.assertEqual("repo-key-0002", env["OPENROUTER_API_KEY"])
        self.assertEqual("bash", env["SLOP_HAUS_TOKEN_IMPL"], "the supervisor's token.sh reads it")
        self.assertEqual(["SLOP_HAUS_TOKEN_IMPL"], [k for k in env if k.startswith("SLOP_HAUS_")])

    def test_no_provider_login_or_credential_comes_from_the_hosts_environment_or_home(self) -> None:
        home = "/home/someone"
        environ = {
            "PATH": "/usr/bin", "HOME": home,
            "CLAUDE_CONFIG_DIR": f"{home}/.claude", "CODEX_HOME": f"{home}/.codex", "PI_CODING_AGENT_DIR": f"{home}/.pi/agent",
            "OPENAI_API_KEY": "host-openai-0006", "CODEX_API_KEY": "host-codex-0007", "ANTHROPIC_API_KEY": "host-anthropic-0008",
            "ANTHROPIC_AUTH_TOKEN": "host-auth-0009", "OPENROUTER_API_KEY": "host-openrouter-0010", "CLAUDE_CODE_OAUTH_TOKEN": "host-login-0011",
        }
        (self.layout.agent_dir("w1") / "secrets.env").write_text("OPENAI_API_KEY=w1-openai-0012\n")
        self.layout.secrets_file.write_text("")
        self.layout.repo_env_file.write_text("")

        base = wiring.review_env(self.layout, environ)("w1")
        settings = ReviewSettings(Path("/r/review.sh"), self.layout.state_dir / "reviews", "https://s", Path("/t"), ReviewLogins.under(self.layout.root))
        env = settings.logins.scoped(review_environment(settings, base), self.layout.state_dir / "reviews" / "w1" / "tmp")

        self.assertEqual({"PATH": "/usr/bin", "HOME": home, "OPENAI_API_KEY": "w1-openai-0012"}, base, "only the agent's layers' credentials")
        self.assertEqual([], [k for k, v in env.items() if k != "HOME" and home in v])
        self.assertIn("CLAUDE_CODE_OAUTH_TOKEN", settings.logins.missing(env, "w1"), "the host's token is no login")

    def test_the_reviewer_takes_its_script_from_run_env_and_posts_with_the_supervisors_token(self) -> None:
        config = SupervisorConfig("https://slop.example", "t", "acme")
        unset = wiring.build_reviewer(self.layout, config, {}, logging.getLogger("test"), SubprocessRunner())
        self.assertIn("SLOP_HAUS_REVIEW_SCRIPT is not set", unset.missing())

        script = self.layout.root / "review.sh"
        script.write_text("#!/bin/sh\n")
        script.chmod(0o755)
        reviewer = wiring.build_reviewer(self.layout, config, {"SLOP_HAUS_REVIEW_SCRIPT": str(script)}, logging.getLogger("test"), SubprocessRunner())
        self.assertIn("not signed in", reviewer.missing())

        token = self.layout.supervisor_dir / ".slop-haus" / "token.sh"
        token.parent.mkdir(parents=True)
        token.write_text("#!/bin/sh\n")
        self.assertEqual("", reviewer.missing())


class ReviewWiringOnAnInsecureServerTest(unittest.TestCase):
    """A server the supervisor won't send credentials to refuses reviews; it must not stop the supervisor."""

    def test_the_reviewer_builds(self) -> None:
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        layout = Layout(Path(tmp.name))
        token = layout.supervisor_dir / ".slop-haus" / "token.sh"
        token.parent.mkdir(parents=True)
        token.write_text("#!/bin/sh\n")
        config = SupervisorConfig("http://frontend:80", "t", "acme")

        # It used to raise here, building a transport the server's scheme refuses.
        reviewer = wiring.build_reviewer(layout, config, {}, logging.getLogger("test"), SubprocessRunner())
        self.assertIn("SLOP_HAUS_REVIEW_SCRIPT is not set", reviewer.missing())
