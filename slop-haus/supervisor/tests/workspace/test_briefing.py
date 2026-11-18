from __future__ import annotations

from collections.abc import Mapping

import logging
import tempfile
import unittest
from pathlib import Path
from typing import cast

from slop_haus_supervisor.ports import ProcessResult, SubprocessRunner
from slop_haus_supervisor.workspace.briefing import BRIEFING, BriefingUnavailable, SlotBriefing
from slop_haus_supervisor.workspace.provisioned import UPDATER
from slop_haus_supervisor.workspace.reconcile import WorkspaceReconciler


class FakeUpdater:
    """Stands in for update.sh: rewrites the briefing as the server's update would."""

    def __init__(self, slot: Path, writes: str | None = None, code: int = 0, raises: bool = False) -> None:
        self.slot, self.writes, self.code, self.raises = slot, writes, code, raises
        self.calls: list[tuple[list[str], Path]] = []
        self.timeouts: list[float | None] = []

    def run(self, argv: list[str], cwd: Path, timeout: float | None = None, env: Mapping[str, str] | None = None) -> ProcessResult:
        self.calls.append((argv, cwd))
        self.timeouts.append(timeout)
        if self.raises:
            raise FileNotFoundError(argv[0])
        if self.writes is not None:
            (self.slot / BRIEFING).write_text(self.writes)
        return ProcessResult(self.code, "", "")


class SlotBriefingTest(unittest.TestCase):
    def setUp(self) -> None:
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.slot = Path(tmp.name)
        (self.slot / BRIEFING).parent.mkdir()
        (self.slot / BRIEFING).write_text("old briefing\n")

    def briefing(self, updater: FakeUpdater) -> SlotBriefing:
        return SlotBriefing(updater, logging.getLogger("test"))

    def test_refreshes_before_reading(self) -> None:
        updater = FakeUpdater(self.slot, writes="new briefing\n")

        text = self.briefing(updater).load(self.slot)

        self.assertEqual([([str(self.slot / UPDATER)], self.slot)], updater.calls)
        self.assertIsNotNone(updater.timeouts[0])
        self.assertEqual("new briefing\n", text)

    def test_a_failed_refresh_carries_on_with_the_existing_files(self) -> None:
        for updater in (FakeUpdater(self.slot, code=1), FakeUpdater(self.slot, raises=True)):
            self.assertTrue(self.briefing(updater).load(self.slot).startswith("old briefing"))

    def test_a_missing_or_empty_briefing_is_refused(self) -> None:
        (self.slot / BRIEFING).write_text(" \n")
        with self.assertRaisesRegex(BriefingUnavailable, "empty"):
            self.briefing(FakeUpdater(self.slot)).load(self.slot)
        (self.slot / BRIEFING).unlink()
        with self.assertRaisesRegex(BriefingUnavailable, "not readable"):
            self.briefing(FakeUpdater(self.slot)).load(self.slot)

    def test_a_refresh_that_hangs_is_cut_off_and_the_existing_files_are_used(self) -> None:
        script = self.slot / UPDATER
        script.write_text("#!/bin/sh\nsleep 30\n")
        script.chmod(0o755)
        briefing = SlotBriefing(SubprocessRunner(), logging.getLogger("test"), refresh_timeout=0.2)

        self.assertTrue(briefing.load(self.slot).startswith("old briefing"))


class FakeReconciler:
    def __init__(self, order: list[str], raises: bool = False) -> None:
        self.order, self.raises = order, raises

    def reconcile(self, workspace: Path) -> None:
        self.order.append("reconcile")
        if self.raises:
            raise RuntimeError("boom")


class OrderedUpdater(FakeUpdater):
    def __init__(self, slot: Path, order: list[str]) -> None:
        super().__init__(slot)
        self.order = order

    def run(self, argv: list[str], cwd: Path, timeout: float | None = None, env: Mapping[str, str] | None = None) -> ProcessResult:
        self.order.append("refresh")
        return super().run(argv, cwd, timeout, env)


class SlotBriefingReconcileTest(unittest.TestCase):
    def setUp(self) -> None:
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.slot = Path(tmp.name)
        (self.slot / BRIEFING).parent.mkdir()
        (self.slot / BRIEFING).write_text("old briefing\n")

    def load(self, raises: bool) -> list[str]:
        order: list[str] = []
        briefing = SlotBriefing(OrderedUpdater(self.slot, order), logging.getLogger("test"), reconciler=cast(WorkspaceReconciler, FakeReconciler(order, raises)))
        self.assertTrue(briefing.load(self.slot).startswith("old briefing"))
        return order

    def test_reconciles_before_the_refresh_so_update_sh_clones_into_vacated_paths(self) -> None:
        self.assertEqual(["reconcile", "refresh"], self.load(False))

    def test_a_failing_reconcile_does_not_stop_the_run(self) -> None:
        self.assertEqual(["reconcile", "refresh"], self.load(True))
