from __future__ import annotations

import unittest
from pathlib import Path

from slop_haus_supervisor.harness.variant import Variant
from slop_haus_supervisor.remote_control.host.claude import BACKGROUND_TASKS, RemoteControlClaude


def harness(environ: dict[str, str]) -> RemoteControlClaude:
    return RemoteControlClaude(Path("/w/.slop-haus/conductor"), "https://slop-haus.example", "prompt", environ=environ)


class RemoteControlClaudeEnvTest(unittest.TestCase):
    def test_background_tasks_stay_on_for_the_owners_session(self) -> None:
        env = harness({}).env(Variant())
        self.assertNotIn(BACKGROUND_TASKS, env)

    def test_the_owners_own_setting_is_kept(self) -> None:
        env = harness({BACKGROUND_TASKS: "1"}).env(Variant())
        self.assertEqual("1", env[BACKGROUND_TASKS])

    def test_the_rest_of_the_session_environment_is_the_shared_harnesss(self) -> None:
        env = harness({}).env(Variant())
        self.assertEqual("1", env["CLAUDE_CODE_DISABLE_AUTO_MEMORY"])
        self.assertEqual("/w/.slop-haus/conductor/claude-config", env["CLAUDE_CONFIG_DIR"])
