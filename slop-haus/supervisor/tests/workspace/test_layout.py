from __future__ import annotations

import contextlib
import io
import os
import stat
import subprocess
import unittest.mock
from pathlib import Path

from slop_haus_supervisor.workspace import clone_probe as probe_module
from slop_haus_supervisor.workspace import config as cfg
from slop_haus_supervisor.workspace import layout as lay
from slop_haus_supervisor.workspace.clone_probe import ProbeResult
from slop_haus_supervisor.workspace.config import SupervisorConfig
from slop_haus_supervisor.workspace.layout import Layout
from slop_haus_supervisor.workspace.update_script import UpdateScript
from support import PYPROJECT, TempDirTestCase, make_archive

CONFIG = SupervisorConfig("https://slop-haus.example", "tenant-uuid", "acme")


class SetUpTest(TempDirTestCase):
    def setUp(self) -> None:
        super().setUp()
        self.layout = Layout(self.tmp / "acme")
        self.archive = make_archive(self.tmp / "package.tar.gz")

    def test_creates_the_layout(self) -> None:
        lay.set_up(self.layout, CONFIG, self.archive)

        self.assertTrue((self.layout.app / "src" / "pkg" / "__init__.py").is_file())
        self.assertTrue(self.layout.harness_dir("claude").is_dir())
        self.assertTrue(self.layout.harness_dir("pi").is_dir())
        self.assertTrue(self.layout.slots.is_dir())
        self.assertEqual(CONFIG, cfg.load(self.layout.config_file))

    def test_secrets_are_private_and_hold_a_placeholder(self) -> None:
        lay.set_up(self.layout, CONFIG, self.archive)

        self.assertEqual(0o600, stat.S_IMODE(self.layout.secrets_file.stat().st_mode))
        self.assertIn("CLAUDE_CODE_OAUTH_TOKEN=", self.layout.secrets_file.read_text())

    def test_rerunning_keeps_secrets_and_slots_and_refreshes_the_package(self) -> None:
        lay.set_up(self.layout, CONFIG, self.archive)
        self.layout.secrets_file.write_text("CLAUDE_CODE_OAUTH_TOKEN=keep-me\n")
        self.layout.secrets_file.chmod(0o644)
        self.layout.config_file.write_text(CONFIG.with_slot("w1").dumps())
        newer = make_archive(self.tmp / "new.tar.gz", {"pyproject.toml": PYPROJECT, "src/pkg/__init__.py": b"V = 2\n"})

        lay.set_up(self.layout, SupervisorConfig("https://moved.example", "tenant-uuid", "acme"), newer)

        self.assertEqual("CLAUDE_CODE_OAUTH_TOKEN=keep-me\n", self.layout.secrets_file.read_text())
        self.assertEqual(0o600, stat.S_IMODE(self.layout.secrets_file.stat().st_mode))
        loaded = cfg.load(self.layout.config_file)
        self.assertEqual(("w1",), loaded.slots)
        self.assertEqual("https://moved.example", loaded.server)
        self.assertEqual("V = 2\n", (self.layout.app / "src" / "pkg" / "__init__.py").read_text())

    def test_refuses_a_root_that_belongs_to_another_tenant(self) -> None:
        lay.set_up(self.layout, CONFIG, self.archive)
        newer = make_archive(self.tmp / "new.tar.gz", {"pyproject.toml": PYPROJECT, "src/pkg/__init__.py": b"V = 2\n"})

        with self.assertRaises(cfg.ConfigError):
            lay.set_up(self.layout, SupervisorConfig("https://x", "other-uuid", "acme"), newer)
        self.assertEqual("V = 1\n", (self.layout.app / "src" / "pkg" / "__init__.py").read_text())

    def test_an_unknown_harness_is_refused_before_anything_is_written(self) -> None:
        with self.assertRaises(cfg.ConfigError):
            lay.set_up(self.layout, SupervisorConfig("https://x", "t", "acme", harness="gpt"), self.archive)
        self.assertFalse(self.layout.root.exists())


class AddSlotTest(TempDirTestCase):
    def setUp(self) -> None:
        super().setUp()
        self.layout = Layout(self.tmp / "acme")
        lay.set_up(self.layout, CONFIG, make_archive(self.tmp / "package.tar.gz"))
        self.script = self.tmp / "setup.sh"
        self.script.write_text("#!/bin/bash\n")
        self.calls: list[tuple[list[str], Path]] = []

    def runner(self, status: int = 0):
        def run(argv: list[str], cwd: Path) -> int:
            # A command that needs its directory fails without it, as a real one would.
            if not cwd.is_dir():
                return 127
            self.calls.append((argv, cwd))
            (cwd / "setup-ran").write_text("x")
            return status

        return run

    def test_runs_the_script_in_the_slot_and_records_it(self) -> None:
        lay.add_slot(self.layout, "w1", self.script, self.runner())

        slot = self.layout.slot("w1")
        # The product resolves the script path, and macOS temp dirs sit under a /var symlink.
        self.assertEqual([(["bash", str(self.script.resolve())], slot)], self.calls)
        self.assertEqual(("w1",), cfg.load(self.layout.config_file).slots)
        self.assertTrue((slot / "setup-ran").is_file())

    def test_a_failed_setup_is_not_recorded_and_can_be_retried(self) -> None:
        with self.assertRaises(cfg.ConfigError):
            lay.add_slot(self.layout, "w1", self.script, self.runner(status=1))
        self.assertEqual((), cfg.load(self.layout.config_file).slots)

        lay.add_slot(self.layout, "w1", self.script, self.runner())
        self.assertEqual(("w1",), cfg.load(self.layout.config_file).slots)

    def test_a_slot_recorded_while_this_one_signed_in_is_kept(self) -> None:
        other = self.layout.config_file

        def run(argv: list[str], cwd: Path) -> int:
            if argv[0] == "bash":
                # Another `slot add` finishes during this one's device sign-in.
                other.write_text(cfg.load(other).with_slot("w2").dumps())
            return 0

        lay.add_slot(self.layout, "w1", self.script, run)

        self.assertEqual(("w2", "w1"), cfg.load(self.layout.config_file).slots)

    def test_an_existing_slot_is_refused(self) -> None:
        lay.add_slot(self.layout, "w1", self.script, self.runner())
        with self.assertRaises(cfg.ConfigError):
            lay.add_slot(self.layout, "w1", self.script, self.runner())

    def test_a_name_that_is_a_path_is_refused(self) -> None:
        for name in ("../escape", "a/b", ".hidden", ""):
            with self.subTest(name=name), self.assertRaises(cfg.ConfigError):
                lay.add_slot(self.layout, name, self.script, self.runner())
        self.assertEqual([], self.calls)


class CloneProtocolTest(TempDirTestCase):
    def test_ssh_is_chosen_by_env_or_by_a_sibling_slot(self) -> None:
        layout = Layout(self.tmp / "acme")
        lay.set_up(layout, CONFIG, make_archive(self.tmp / "package.tar.gz"))
        sibling = layout.slot("w0")
        git(self.tmp, "init", "-q", str(sibling))
        git(sibling, "remote", "add", "origin", "git@git.example:acme/other.git")
        config = cfg.load(layout.config_file).with_slot("w0")
        with unittest.mock.patch.dict(os.environ, {"SLOP_HAUS_CLONE_PROTOCOL": ""}):
            self.assertEqual("ssh", lay._clone_protocol(layout, config))  # pyright: ignore[reportPrivateUsage]
            self.assertEqual("https", lay._clone_protocol(layout, cfg.load(layout.config_file)))  # pyright: ignore[reportPrivateUsage]
        with unittest.mock.patch.dict(os.environ, {"SLOP_HAUS_CLONE_PROTOCOL": "ftp"}), self.assertRaises(cfg.ConfigError):
            lay._clone_protocol(layout, config)  # pyright: ignore[reportPrivateUsage]

    def test_a_probed_choice_is_stored_and_printed_with_its_reason_and_probed_once(self) -> None:
        layout = Layout(self.tmp / "acme")
        lay.set_up(layout, CONFIG, make_archive(self.tmp / "package.tar.gz"))
        probed: list[str] = []

        def probe(url: str) -> ProbeResult:
            probed.append(url)
            return ProbeResult(False, "auth refused: Permission denied (publickey)")

        out = io.StringIO()
        with unittest.mock.patch.dict(os.environ, {"SLOP_HAUS_CLONE_PROTOCOL": ""}), contextlib.redirect_stdout(out):
            first = lay._clone_protocol(layout, cfg.load(layout.config_file), "git.example/acme/app", ssh_works=probe)  # pyright: ignore[reportPrivateUsage]
            second = lay._clone_protocol(layout, cfg.load(layout.config_file), "git.example/acme/app", ssh_works=probe)  # pyright: ignore[reportPrivateUsage]

        self.assertEqual(("https", "https"), (first, second))
        self.assertEqual(["git@git.example:acme/app.git"], probed)
        self.assertEqual("https", cfg.load(layout.config_file).clone_protocol)
        self.assertIn("https (auth refused: Permission denied (publickey))", out.getvalue())

    def test_a_hosts_chooser_reads_the_stored_protocol_at_each_choice_and_probes_once(self) -> None:
        layout = Layout(self.tmp / "acme")
        lay.set_up(layout, CONFIG, make_archive(self.tmp / "package.tar.gz"))
        probed: list[str] = []

        def probe(url: str) -> ProbeResult:
            probed.append(url)
            return ProbeResult(True, "test")

        choose = lay.clone_protocol_chooser(layout, ssh_works=probe)
        script = UpdateScript("", (("app", "git.example/acme/app"),))
        with unittest.mock.patch.dict(os.environ, {"SLOP_HAUS_CLONE_PROTOCOL": ""}), contextlib.redirect_stdout(io.StringIO()):
            self.assertIsNone(choose(UpdateScript("", ())), "a script that clones nothing needs no protocol")
            self.assertEqual("ssh", choose(script))
            self.assertEqual("ssh", choose(script))
            # Stored after the chooser was made (a slot's probe, or setup): it is the one used.
            layout.config_file.write_text(cfg.load(layout.config_file).with_clone_protocol("https").dumps())
            self.assertEqual("https", choose(script))
        self.assertEqual(["git@git.example:acme/app.git"], probed)

    def test_a_choice_stored_while_this_one_probed_stands(self) -> None:
        layout = Layout(self.tmp / "acme")
        lay.set_up(layout, CONFIG, make_archive(self.tmp / "package.tar.gz"))

        def probe(url: str) -> ProbeResult:
            # Another workspace's probe finishes first.
            layout.config_file.write_text(cfg.load(layout.config_file).with_clone_protocol("https").dumps())
            return ProbeResult(True, "test")

        with unittest.mock.patch.dict(os.environ, {"SLOP_HAUS_CLONE_PROTOCOL": ""}), contextlib.redirect_stdout(io.StringIO()):
            chosen = lay._clone_protocol(layout, cfg.load(layout.config_file), "git.example/acme/app", ssh_works=probe)  # pyright: ignore[reportPrivateUsage]

        self.assertEqual("https", chosen)
        self.assertEqual("https", cfg.load(layout.config_file).clone_protocol)


class SshProbeTest(unittest.TestCase):
    def probe(self, run: unittest.mock.Mock) -> ProbeResult:
        with unittest.mock.patch.object(probe_module.subprocess, "run", run):
            return probe_module.ssh_works("git@h:o/r.git")

    def done(self, code: int, stderr: str = "") -> unittest.mock.Mock:
        return unittest.mock.Mock(return_value=subprocess.CompletedProcess([], code, "", stderr))

    def test_success(self) -> None:
        self.assertTrue(self.probe(self.done(0)).works)

    def test_auth_refused_and_other_exits_say_why(self) -> None:
        refused = self.probe(self.done(128, "git@h: Permission denied (publickey).\nfatal: x"))
        other = self.probe(self.done(2, "boom\nmore"))

        self.assertEqual((False, "auth refused: git@h: Permission denied (publickey)."), (refused.works, refused.reason))
        self.assertEqual((False, "exit 2: boom"), (other.works, other.reason))

    def test_a_timeout_says_so(self) -> None:
        result = self.probe(unittest.mock.Mock(side_effect=subprocess.TimeoutExpired("git", 30)))

        self.assertFalse(result.works)
        self.assertIn("timed out", result.reason)


def git(cwd: Path, *args: str) -> str:
    return subprocess.run(["git", *args], cwd=cwd, check=True, capture_output=True, text=True).stdout.strip()
