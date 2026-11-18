from __future__ import annotations

import json
import tempfile
import unittest
from pathlib import Path
from unittest import mock

from slop_haus_supervisor.fleet.host_slot import SHARED_LOGIN_ROOT
from slop_haus_supervisor.remote_control.host import main as main_module
from slop_haus_supervisor.remote_control.host.claude import RemoteControlClaude
from slop_haus_supervisor.remote_control.host.codex import RemoteControlCodex
from slop_haus_supervisor.remote_control.host.codex_records import CodexRecords
from slop_haus_supervisor.usage.billing import codex_login_billing_mode
from slop_haus_supervisor.remote_control.pi.harness import RemoteControlPi
from slop_haus_supervisor.remote_control.host.main import ChosenHarness


class SharedLoginTest(unittest.TestCase):
    """A host the supervisor started uses its harness logins; a host started by hand keeps its own."""

    def setUp(self) -> None:
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.root = Path(tmp.name) / "supervisor"
        self.workspace = Path(tmp.name) / "workspace"
        self.state = self.workspace / ".slop-haus" / "conductor"
        (self.workspace / ".slop-haus" / "pi").mkdir(parents=True)
        (self.workspace / ".slop-haus" / "pi" / "slop-haus.ts").write_text("")

    def env(self, harness: str, supervised: bool, environ: dict[str, str] | None = None) -> dict[str, str]:
        chosen = self.choose(harness, supervised, environ).harness
        if isinstance(chosen, RemoteControlClaude):
            return chosen.env(mock.MagicMock(model="", effort="", subagent_model=""))
        assert isinstance(chosen, (RemoteControlPi, RemoteControlCodex))
        return chosen.env()

    def choose(self, harness: str, supervised: bool, environ: dict[str, str] | None = None) -> ChosenHarness:
        args = main_module.argparse.Namespace(harness=harness, provider="", model="", thinking="", supervised=supervised)
        env = {SHARED_LOGIN_ROOT: str(self.root)} if environ is None else environ
        return main_module.choose_harness(args, self.workspace, self.state, "https://s", self.workspace / "t.sh", main_module.Settings(), env)

    def test_pi_and_codex_use_the_supervisors_login_folders(self) -> None:
        with mock.patch.dict("os.environ", {}, clear=True):
            self.assertEqual(str(self.root / "harness" / "pi"), self.env("pi", True)["PI_CODING_AGENT_DIR"])
            self.assertEqual(str(self.root / "harness" / "codex"), self.env("codex", True)["CODEX_HOME"])

    def test_a_supervised_codex_host_takes_a_locked_copy_only_under_the_owners_flag(self) -> None:
        with mock.patch.dict("os.environ", {}, clear=True):
            env = {SHARED_LOGIN_ROOT: str(self.root), "SLOP_HAUS_CODEX_SHARED_LOGIN": "verified"}
            copy = self.env("codex", True, env)
            self.assertEqual(str(self.root / "harness" / "codex"), self.env("codex", True)["CODEX_HOME"])
        chosen = self.choose("codex", True, env).harness
        assert isinstance(chosen, RemoteControlCodex)
        self.assertEqual(str(self.root / "harness" / "codex"), copy["CODEX_HOME"])
        self.assertEqual(self.root / "harness" / "codex-slots" / "a", chosen.home_for("a"))

    def test_a_host_started_by_hand_keeps_its_own_folders(self) -> None:
        with mock.patch.dict("os.environ", {}, clear=True):
            self.assertEqual(str(self.state / "pi-agent"), self.env("pi", False)["PI_CODING_AGENT_DIR"])
            self.assertEqual(str(self.state / "codex-home"), self.env("codex", False)["CODEX_HOME"])
            self.assertEqual(str(self.state / "pi-agent"), self.env("pi", True, {})["PI_CODING_AGENT_DIR"])

    def test_claude_keeps_its_own_config_folder_and_the_layers_token_reaches_it(self) -> None:
        with mock.patch.dict("os.environ", {"CLAUDE_CODE_OAUTH_TOKEN": "layer-token-0001"}, clear=True):
            env = self.env("claude", True)
        self.assertEqual(str(self.state / "claude-config"), env["CLAUDE_CONFIG_DIR"])
        self.assertEqual("layer-token-0001", env["CLAUDE_CODE_OAUTH_TOKEN"])

    def test_a_provider_key_in_the_remote_control_layer_still_reaches_pi(self) -> None:
        with mock.patch.dict("os.environ", {"OPENAI_API_KEY": "layer-key-0002"}, clear=True):
            env = self.env("pi", True)
        self.assertEqual("layer-key-0002", env["OPENAI_API_KEY"])

    def test_codex_billing_is_read_from_the_shared_home_with_a_workers_session_in_it(self) -> None:
        home = self.root / "harness" / "codex"
        (home / "sessions" / "2026" / "10" / "29").mkdir(parents=True)
        (home / "sessions" / "2026" / "10" / "29" / "rollout-worker.jsonl").write_text("{}\n")
        (home / "auth.json").write_text(json.dumps({"auth_mode": "chatgpt", "tokens": {"a": "b"}}))
        with mock.patch.dict("os.environ", {}, clear=True):
            chosen = self.choose("codex", True)
        assert chosen.records is not None
        records = chosen.records(mock.MagicMock())

        assert isinstance(records, CodexRecords)
        self.assertEqual(home, records.config_dir)
        self.assertEqual("subscription", codex_login_billing_mode(records.config_dir))


if __name__ == "__main__":
    unittest.main()
