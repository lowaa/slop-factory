from __future__ import annotations

import logging
import threading
import unittest
from pathlib import Path
from typing import Any

from slop_haus_supervisor.remote_control.host.layout_reporter import LAYOUT_PATH, LayoutReporter
from slop_haus_supervisor.remote_control.wire.client import Client
from wire_fixtures import FakeTokens, StubServer

ROOT = Path("/workspace")


def layout(*repos: str) -> dict[str, Any]:
    return {
        "root": {"path": ".", "git": False, "origin": ""},
        "repos": [{"path": r, "git": True, "origin": f"example.com/org/{r}.git"} for r in repos],
    }


class LayoutReporterTest(unittest.TestCase):
    def setUp(self) -> None:
        self.answers: list[Any] = []
        self.server = StubServer(lambda _p, _b, _h: self.answers.pop(0) if self.answers else (204, {}))
        self.addCleanup(self.server.close)
        self.current: dict[str, Any] | None = layout("app")
        self.stop = threading.Event()
        self.reporter = LayoutReporter(Client(self.server.url, FakeTokens()), ROOT, self.stop,
                                       layout_of=lambda _root: self.current)

    def bodies(self) -> list[Any]:
        return [r["body"] for r in self.server.requests if r["path"] == LAYOUT_PATH]

    def test_sends_the_layout_to_the_conductor_route(self) -> None:
        self.assertTrue(self.reporter.report_once())
        self.assertEqual([layout("app")], self.bodies())
        (request,) = self.server.requests
        self.assertEqual("Bearer token-1", request["headers"]["authorization"])

    def test_an_unchanged_layout_is_not_sent_again_but_a_changed_one_is(self) -> None:
        self.reporter.report_once()
        self.reporter.report_once()
        self.current = layout("app", "other")
        self.reporter.report_once()
        self.assertEqual([layout("app"), layout("app", "other")], self.bodies())

    def test_a_refused_report_is_sent_again_at_the_next_look(self) -> None:
        self.answers.append((500, {"detail": "boom"}))
        with self.assertLogs(level=logging.WARNING):
            self.assertFalse(self.reporter.report_once())
        self.assertTrue(self.reporter.report_once())
        self.assertEqual([layout("app")] * 2, self.bodies())

    def test_a_layout_git_could_not_tell_is_not_reported(self) -> None:
        self.current = None
        self.assertFalse(self.reporter.report_once())
        self.assertEqual([], self.server.requests)

    def test_a_reader_that_raises_does_not_stop_the_host(self) -> None:
        def broken(_root: Path) -> dict[str, Any]:
            raise RuntimeError("no")

        self.reporter.layout_of = broken
        with self.assertLogs(level=logging.WARNING):
            self.assertFalse(self.reporter.report_once())

    def test_the_loop_reports_at_start_and_ends_on_stop(self) -> None:
        self.reporter.interval = 0.01
        thread = self.reporter.start()
        with self.server.arrived:
            self.server.arrived.wait_for(lambda: self.server.requests, timeout=30)
        self.stop.set()
        thread.join(30)
        self.assertFalse(thread.is_alive())
        self.assertEqual([layout("app")], self.bodies())


if __name__ == "__main__":
    unittest.main()
