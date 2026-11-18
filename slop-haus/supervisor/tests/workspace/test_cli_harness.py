from __future__ import annotations

import os
import unittest
import unittest.mock

from slop_haus_supervisor.workspace.envfile import EnvFile
from test_layered_env_cli import LayeredCliTest

CODEX = """#!/bin/sh
[ -n "$FAKE_OK" ] && printf '{"auth_mode":"chatgpt","tokens":{"id_token":"x"}}' > "$CODEX_HOME/auth.json"
exit 0
"""


@unittest.skipIf(os.geteuid() == 0, "harness login refuses root")
class HarnessCliTest(LayeredCliTest):
    def setUp(self) -> None:
        super().setUp()
        self.bin = self.tmp / "bin"
        self.bin.mkdir()
        (self.bin / "codex").write_text(CODEX)
        (self.bin / "codex").chmod(0o755)

    def environ(self, ok: bool) -> unittest.mock._patch_dict:  # pyright: ignore[reportPrivateUsage]
        return unittest.mock.patch.dict(os.environ, {"PATH": f"{self.bin}:/usr/bin:/bin", **({"FAKE_OK": "1"} if ok else {})})

    def test_status_lists_every_harness_and_marks_the_roots_as_used(self) -> None:
        code, out, _ = self.cmd("harness", "status")

        self.assertEqual(0, code)
        self.assertIn("  claude: MISSING", out)
        self.assertNotIn("claude: MISSING: CLAUDE_CODE_OAUTH_TOKEN or ANTHROPIC_API_KEY (not used", out)
        self.assertIn("(not used by any slot)", out.split("pi:")[1])

    def test_login_logs_the_harness_in_and_a_broken_login_exits_non_zero(self) -> None:
        with self.environ(ok=False):
            code, out, _ = self.cmd("harness", "login", "codex")
        self.assertEqual(1, code)
        self.assertIn("still not logged in", out)

        with self.environ(ok=True):
            code, out, _ = self.cmd("harness", "login", "codex")
        self.assertEqual(0, code)
        self.assertIn("codex is logged in", out)
        self.assertIn("codex: logged in", self.cmd("harness", "status")[1])

    def test_an_unknown_harness_is_refused_by_the_parser(self) -> None:
        with self.assertRaises(SystemExit):
            self.cmd("harness", "login", "nope")

    def test_the_tenant_status_includes_the_harness_logins(self) -> None:
        _, out, _ = self.cmd("status")

        self.assertIn("harness logins:", out)

    def test_a_claude_token_lands_in_secrets_env_through_the_cli(self) -> None:
        (self.bin / "claude").write_text("#!/bin/sh\necho 'token: sk-ant-oat01-ABCDEFGHIJKLMNOPQRSTUV'\n")
        (self.bin / "claude").chmod(0o755)
        with self.environ(ok=False):
            code, out, _ = self.cmd("harness", "login", "claude")

        self.assertEqual(0, code)
        self.assertNotIn("ABCDEFGHIJKL", out)
        self.assertEqual("sk-ant-oat01-ABCDEFGHIJKLMNOPQRSTUV", EnvFile(self.root / "secrets.env").values()["CLAUDE_CODE_OAUTH_TOKEN"])
