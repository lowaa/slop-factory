from __future__ import annotations

import tempfile
import unittest
from pathlib import Path

from slop_haus_supervisor.service.instance_lock import AlreadyRunning, InstanceLock


class InstanceLockOwnerTest(unittest.TestCase):
    def test_a_second_taker_is_told_who_holds_the_lock(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "x.lock"
            with InstanceLock(path, "supervisor"):
                with self.assertRaises(AlreadyRunning) as refused:
                    InstanceLock(path, "host").__enter__()
        self.assertEqual("supervisor", refused.exception.owner)

    def test_an_unnamed_holder_leaves_the_owner_empty(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "x.lock"
            with InstanceLock(path):
                with self.assertRaises(AlreadyRunning) as refused:
                    InstanceLock(path).__enter__()
        self.assertEqual("", refused.exception.owner)

    def test_the_lock_is_free_again_once_released(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "x.lock"
            with InstanceLock(path, "supervisor"):
                pass
            with InstanceLock(path, "host"):
                pass
