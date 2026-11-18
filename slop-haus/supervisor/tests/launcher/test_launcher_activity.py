from __future__ import annotations

import unittest
from datetime import datetime

from slop_haus_supervisor.launcher.activity import parse


def stamp(text: str) -> float:
    return datetime.strptime(text, "%Y-%m-%d %H:%M:%S").timestamp()


class ActivityTest(unittest.TestCase):
    def test_latest_phase_of_each_slot_and_the_last_error(self) -> None:
        activity = parse(
            [
                "2026-10-22 10:00:00,123 INFO slop-haus-supervisor: running 2 slot(s) for https://s.example",
                "2026-10-22 10:00:01,000 INFO slop-haus-supervisor: box-1: phase idle -> working (ticket #12)",
                "2026-10-22 10:00:02,000 INFO slop-haus-supervisor: box-2: phase idle -> working (ticket #13)",
                "2026-10-22 10:05:00,000 ERROR slop-haus-supervisor: box-2: boom",
                "2026-10-22 10:06:00,000 INFO slop-haus-supervisor: box-1: phase working -> idle",
                "a traceback line that is no log line",
            ]
        )

        by_slot = {s.slot: s for s in activity.slots}
        self.assertEqual(("idle", "", stamp("2026-10-22 10:06:00")), (by_slot["box-1"].phase, by_slot["box-1"].ticket, by_slot["box-1"].since))
        self.assertEqual(("working", "(ticket #13)"), (by_slot["box-2"].phase, by_slot["box-2"].ticket))
        assert activity.last_error is not None
        self.assertEqual("box-2: boom", activity.last_error.message)

    def test_key_value_phase_lines_and_the_older_form_are_both_read(self) -> None:
        activity = parse(
            [
                "2026-10-22 10:00:01,000 INFO slop-haus-supervisor: box-1: phase idle -> working (ticket #12)",
                "2026-10-22 10:00:02,000 INFO slop-haus-supervisor: slot=box-1 phase=running from=idle ticket=13",
                "2026-10-22 10:00:03,000 INFO slop-haus-supervisor: slot=box-2 phase=idle from=running",
            ]
        )

        by_slot = {s.slot: s for s in activity.slots}
        self.assertEqual(("running", "(ticket #13)"), (by_slot["box-1"].phase, by_slot["box-1"].ticket))
        self.assertEqual(("idle", ""), (by_slot["box-2"].phase, by_slot["box-2"].ticket))

    def test_lines_from_before_the_latest_start_are_forgotten(self) -> None:
        activity = parse(
            [
                "2026-10-22 09:00:00,000 INFO slop-haus-supervisor: old-1: phase idle -> working (ticket #1)",
                "2026-10-22 09:00:01,000 ERROR slop-haus-supervisor: old failure",
                "2026-10-22 10:00:00,000 INFO slop-haus-supervisor: running 1 slot(s) for https://s.example",
            ]
        )

        self.assertEqual((), activity.slots)
        self.assertIsNone(activity.last_error)
