from __future__ import annotations

import os
import tempfile
import threading
import unittest
from pathlib import Path

from slop_haus_supervisor.service import paused_state
from slop_haus_supervisor.service.control import ControlSpool, ControlTimeout


class ControlSpoolTest(unittest.TestCase):
    def setUp(self) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.spool = ControlSpool(Path(self._tmp.name) / "control")

    def test_request_reaches_the_handler_and_the_answer_returns(self) -> None:
        seen: list[list[str] | None] = []
        answered = threading.Event()

        def serve() -> None:
            while not self.spool.serve_pending(lambda names: (seen.append(names), {"a": "", "b": "still dirty"})[1]):
                pass
            answered.set()

        threading.Thread(target=serve, daemon=True).start()
        answers = self.spool.request_unpause(["a", "b"], timeout=10)
        self.assertTrue(answered.wait(10))
        self.assertEqual(answers, {"a": "", "b": "still dirty"})
        self.assertEqual(seen, [["a", "b"]])
        self.assertEqual(list((self.spool.directory / "requests").iterdir()), [])
        self.assertEqual(list((self.spool.directory / "replies").iterdir()), [])

    def test_all_slots_is_sent_as_none(self) -> None:
        seen: list[list[str] | None] = []

        def record(names: list[str] | None) -> dict[str, str]:
            seen.append(names)
            return {}

        def serve() -> None:
            while not self.spool.serve_pending(record):
                pass

        threading.Thread(target=serve, daemon=True).start()
        self.spool.request_unpause(None, timeout=10)
        self.assertEqual(seen, [None])

    def test_no_supervisor_times_out(self) -> None:
        ticks = iter(range(100))
        with self.assertRaises(ControlTimeout):
            self.spool.request_unpause(["a"], timeout=3, sleep=lambda _s: None, monotonic=lambda: float(next(ticks)))

    def test_files_that_are_not_requests_are_ignored(self) -> None:
        self.spool.prepare()
        (self.spool.directory / "requests" / "evil.json").write_text('{"slots": ["x"]}')
        self.assertEqual(self.spool.serve_pending(lambda _n: {}), 0)

    def test_stale_requests_and_replies_are_swept_and_fresh_ones_kept(self) -> None:
        self.spool.prepare()
        old_request = self.spool.directory / "requests" / ("a" * 32 + ".json")
        old_reply = self.spool.directory / "replies" / ("b" * 32 + ".json")
        fresh = self.spool.directory / "requests" / ("c" * 32 + ".json")
        for path in (old_request, old_reply, fresh):
            path.write_text("{}")
        for path in (old_request, old_reply):
            os.utime(path, (1.0, 1.0))
        self.spool.sweep_stale()
        self.assertEqual([False, False, True], [old_request.exists(), old_reply.exists(), fresh.exists()])

    def test_state_dir_is_private(self) -> None:
        self.spool.prepare()
        self.assertEqual((self.spool.directory).stat().st_mode & 0o077, 0)


class PausedStateTest(unittest.TestCase):
    def test_round_trip_and_missing_file(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "state" / "paused.json"
            self.assertEqual(paused_state.read(path), [])
            paused = [paused_state.PausedSlot("s1", "dirty", 5.0)]
            paused_state.write(path, paused)
            self.assertEqual(paused_state.read(path), paused)
