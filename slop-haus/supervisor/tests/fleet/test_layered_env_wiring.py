from __future__ import annotations

import logging
import tempfile
import threading
import unittest
from pathlib import Path
from unittest import mock

from slop_haus_supervisor.fleet import wiring
from slop_haus_supervisor.fleet.slot import Slot
from slop_haus_supervisor.harness.base import ConfiguredHarness
from slop_haus_supervisor.harness.factory import HarnessContext, HarnessKind
from slop_haus_supervisor.harness.claude import ClaudeHarness
from slop_haus_supervisor.workspace import config as cfg
from slop_haus_supervisor.workspace.config import SupervisorConfig
from slop_haus_supervisor.workspace.layout import Layout


class LayeredWiringTest(unittest.TestCase):
    def setUp(self) -> None:
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.layout = Layout(Path(tmp.name))
        self.layout.secrets_file.write_text("ANTHROPIC_API_KEY=base-key-0001\n")
        self.layout.repo_env_file.write_text("REVIEW=base-review-0002\n")
        for name in ("w1", "w2"):
            (self.layout.slot(name) / ".git").mkdir(parents=True)
        self.layout.agent_dir("w1").mkdir(parents=True)
        (self.layout.agent_dir("w1") / "repo.env").write_text("OWN=w1-own-0003\n")
        (self.layout.agent_dir("w1") / "secrets.env").write_text("W1_SECRET=w1-secret-0004\n")
        self.layout.remote_control_dir.mkdir()
        (self.layout.remote_control_dir / "repo.env").write_text("RC=rc-only-0005\n")
        (self.layout.remote_control_dir / "secrets.env").write_text("RC_SECRET=rc-secret-0006\n")

    def scheduler(self):
        config = SupervisorConfig("https://s", "t", "acme", slots=("w1", "w2"))
        with mock.patch.object(ClaudeHarness, "check_installed"):
            return wiring.build(self.layout, config, logging.getLogger("test"), threading.Event())

    def test_each_slots_dot_env_and_masker_hold_its_own_layers_and_never_remote_controls(self) -> None:
        scheduler = self.scheduler()

        self.assertEqual("REVIEW=base-review-0002\nOWN=w1-own-0003\n", (self.layout.slot("w1") / ".env").read_text())
        self.assertEqual("REVIEW=base-review-0002\n", (self.layout.slot("w2") / ".env").read_text())
        slots: list[Slot] = scheduler._slots  # pyright: ignore[reportAssignmentType, reportPrivateUsage]
        maskers = {slot.agent: slot._d.masker for slot in slots}  # pyright: ignore[reportPrivateUsage]
        seen = {
            name: [v for v in ("base-review-0002", "w1-own-0003", "w1-secret-0004", "rc-only-0005", "rc-secret-0006") if masker.mask(v) != v]
            for name, masker in maskers.items()
        }
        self.assertEqual(["base-review-0002", "w1-own-0003", "w1-secret-0004"], seen["w1"])
        self.assertEqual(["base-review-0002"], seen["w2"])

    def test_the_harness_login_is_the_base_secrets_only(self) -> None:
        captured: dict[str, str] = {}
        real = wiring.build_harness

        def spy(kind: HarnessKind, harness: str, context: HarnessContext, *, default_variant: str = "") -> ConfiguredHarness:
            captured.update(context.credentials)
            return real(kind, harness, context, default_variant=default_variant)

        with mock.patch.object(wiring, "build_harness", spy):
            self.scheduler()

        self.assertEqual({"ANTHROPIC_API_KEY": "base-key-0001"}, captured)

    def test_the_harness_is_built_with_each_agents_environment_and_masks_what_it_reads(self) -> None:
        captured: list[HarnessContext] = []
        real = wiring.build_harness

        def spy(kind: HarnessKind, harness: str, context: HarnessContext, *, default_variant: str = "") -> ConfiguredHarness:
            captured.append(context)
            return real(kind, harness, context, default_variant=default_variant)

        with mock.patch.object(wiring, "build_harness", spy):
            scheduler = self.scheduler()
        (self.layout.agent_dir("w1") / "secrets.env").write_text("LATE=late-secret-0011\n")

        (context,) = captured
        assert context.agent_env is not None
        self.assertEqual("late-secret-0011", context.agent_env("w1")["LATE"])
        self.assertNotIn("LATE", context.agent_env("w2"))
        slots: list[Slot] = scheduler._slots  # pyright: ignore[reportAssignmentType, reportPrivateUsage]
        w1 = next(slot for slot in slots if slot.agent == "w1")
        self.assertEqual("***", w1._d.masker.mask("late-secret-0011"))  # pyright: ignore[reportPrivateUsage]
        self.assertEqual("***", context.masker.mask("late-secret-0011") if context.masker else "")

    def test_a_forbidden_name_in_a_layer_file_the_slot_reads_stops_the_start(self) -> None:
        (self.layout.agent_dir("w1") / "repo.env").write_text("LD_PRELOAD=x\n")

        with self.assertRaises(Exception) as caught:
            self.scheduler()

        self.assertIn("refusing LD_PRELOAD", str(caught.exception))
        self.assertIsInstance(caught.exception, ValueError | cfg.ConfigError)

    def test_a_rebuild_style_install_names_the_agent_for_a_clone_directory(self) -> None:
        clone = self.layout.slot("w1") / "app"
        (clone / ".git").mkdir(parents=True)

        wiring.install_repo_env(self.layout, [clone], agent="w1")

        self.assertEqual("REVIEW=base-review-0002\nOWN=w1-own-0003\n", (clone / ".env").read_text())


class UnknownSettingWarningTest(unittest.TestCase):
    def test_agent_env_warns_once_per_unknown_name_however_many_runs_read_it(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            layout = Layout(Path(tmp))
            (layout.root / "run.env").write_text("SLOP_HAUS_FUTURE=on\n")
            warnings: list[str] = []
            env = wiring.agent_env(layout, warn=warnings.append)

            for _ in range(3):
                self.assertNotIn("SLOP_HAUS_FUTURE", env("w1"))

            self.assertEqual(1, len(warnings))
            self.assertIn("SLOP_HAUS_FUTURE", warnings[0])
