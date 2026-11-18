"""A harness config the owner changes on the board while a conversation is live: the next message runs on it.

The fake config stands in for ConfigWatch: when told the board changed, the next check says so and puts
the new setting in the environment, as PythonLaunch does for Claude Code, so the fake harness's record of
its environment shows which config a process was started with.
"""

from __future__ import annotations

import os

from host_fixtures import CONV, HostCase, ended, flag_value, make_harness
from slop_haus_supervisor.remote_control.host.host import Host
from slop_haus_supervisor.remote_control.host.settings import Settings

SETTING = "CLAUDE_CODE_EFFORT_LEVEL"


class FakeConfig:
    def __init__(self) -> None:
        self.board: str | None = None
        self.checks = 0

    def change(self, effort: str) -> None:
        self.board = effort

    def changed(self) -> bool:
        self.checks += 1
        if self.board is None:
            return False
        os.environ[SETTING], self.board = self.board, None
        return True


class ConfigChangeTest(HostCase):
    def setUp(self) -> None:
        super().setUp()
        self.config = FakeConfig()
        self.host = Host(self.root, self.reporter, make_harness(self.root, self.script),
                         Settings(harness_grace=5, idle_timeout=300), clock=self.clock, config=self.config)
        self.addCleanup(self.host.shutdown)
        self.addCleanup(os.environ.pop, SETTING, None)

    def started_with(self, index: int) -> str | None:
        return self.invocations(index + 1)[index]["env"].get(SETTING)

    def test_the_next_message_after_a_change_runs_in_a_harness_started_with_it(self) -> None:
        self.start()
        self.converse(2, "hello")
        first = self.live()

        self.config.change("high")
        self.converse(3, "and now")

        self.assertTrue(ended(first), "the harness started with the old config is gone")
        self.assertEqual(2, len(self.invocations(2)))
        self.assertIsNone(self.started_with(0))
        self.assertEqual("high", self.started_with(1))
        self.assertEqual(CONV, flag_value(self.invocations(2)[1]["argv"], "--resume"), "the same session, resumed")
        self.assertIn("echo: and now", self.reporter.said())
        self.assertIn("settings changed on the board", self.reporter.said(), "the owner is told why it restarted")
        self.assertEqual([1, 2], [e["turn"] for e in self.reporter.of("turn_end")])

    def test_with_no_change_the_harness_is_kept(self) -> None:
        self.start()
        self.converse(2, "hello")
        self.converse(3, "again")

        self.assertEqual(1, len(self.invocations()))
        self.assertGreaterEqual(self.config.checks, 2, "asked before each turn")
        self.assertNotIn("settings changed", self.reporter.said())

    def test_a_change_during_a_turn_waits_for_it_to_end(self) -> None:
        self.start()
        self.message(2, "first")
        self.config.change("high")
        self.message(3, "second")

        self.pump(lambda: self.turns() == 2)

        self.assertEqual(["first", "second"], [r["text"] for r in self.received()], "the turn in flight was not cut")
        self.assertEqual("high", self.started_with(1), "the second message went to a harness started with the change")

    def test_a_change_before_the_conversation_starts_needs_no_restart(self) -> None:
        self.config.change("high")
        self.start()
        self.converse(2, "hello")

        self.assertEqual(1, len(self.invocations()))
        self.assertEqual("high", self.started_with(0))
        self.assertNotIn("settings changed", self.reporter.said())

    def test_a_change_while_no_harness_runs_is_taken_up_by_the_next_one(self) -> None:
        self.start()
        self.converse(2, "hello")
        self.clock.advance(301)
        self.host.tick()
        self.assertIsNone(self.conv().live)

        self.config.change("high")
        self.converse(3, "back")

        self.assertEqual("high", self.started_with(1))
        self.assertNotIn("settings changed", self.reporter.said(), "nothing was running to restart")
