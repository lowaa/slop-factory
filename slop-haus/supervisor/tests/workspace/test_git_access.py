from __future__ import annotations

import os
import unittest.mock
from collections.abc import Mapping
from pathlib import Path

from slop_haus_supervisor.workspace.git_access import GitAccess
from support import TempDirTestCase


class GitAccessTest(TempDirTestCase):
    def test_every_command_runs_with_git_prompts_off_and_is_left_as_given(self) -> None:
        seen: list[tuple[list[str], Mapping[str, str]]] = []

        def spawn(argv: list[str], cwd: Path, env: Mapping[str, str]) -> int:
            seen.append((argv, env))
            return 7

        with unittest.mock.patch.dict(os.environ):
            os.environ.pop("GIT_TERMINAL_PROMPT", None)
            code = GitAccess({"K": "v"}, spawn)(["bash", "update.sh"], self.tmp)

        self.assertEqual(7, code)
        self.assertEqual(["bash", "update.sh"], seen[0][0])
        self.assertEqual("0", seen[0][1]["GIT_TERMINAL_PROMPT"])
        self.assertEqual("v", seen[0][1]["K"])
