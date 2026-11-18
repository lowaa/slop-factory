"""What every scenario against a real harness starts from: the stack, the
adapter of the harness under test, and a board and model server to act on."""

from __future__ import annotations

import json
import re
import tempfile
import unittest
from pathlib import Path
from typing import Any

from harnesses import ADAPTERS
from support.board import Board
from support.env import Stack, stack_from_env
from support.harness_run import HarnessRun
from support.model_client import ModelClient
from support.workspace import Workspace

MODEL = "scripted-model-1"
# What a scenario changes the model to on the board.
OTHER_MODEL = "scripted-model-2"


TOKEN_VALUE = re.compile(r'((?:access|refresh)_token\\*"\s*:\s*\\*")[^"\\]*')


def redacted(text: str) -> str:
    return TOKEN_VALUE.sub(r"\1<redacted>", text)


def excerpt(text: str, width: int = 800) -> str:
    return text if len(text) <= width else f"...{text[-width:]}"


class HarnessCase(unittest.TestCase):
    stack: Stack

    @classmethod
    def setUpClass(cls) -> None:
        cls.stack = stack_from_env()
        cls.adapter = ADAPTERS[cls.stack.harness]
        cls.model = ModelClient(cls.stack.model_server)
        cls.model.wait_healthy()
        cls.board = Board(cls.stack.server)
        cls.board.sign_in(cls.stack.email, cls.stack.password)

    def setUp(self) -> None:
        self.tmp = Path(tempfile.mkdtemp(prefix="harness-e2e-"))
        self.logs = self.stack.logs / self._testMethodName
        self.logs.mkdir(parents=True, exist_ok=True)
        self.addCleanup(self.model.reset)
        # Runs before the reset, which forgets what the model was asked.
        self.addCleanup(self.keep_requests)

    def keep_requests(self) -> None:
        """What the model was asked, in the logs test.sh copies out of a failed run."""
        try:
            requests = [vars(r) for r in self.model.conversation()]
            text = json.dumps(requests, indent=1, default=str)
            # A scenario may make the harness read its own workspace credentials
            # (a negative test does, on purpose): not into an artifact.
            (self.logs / "model_requests.json").write_text(redacted(text))
        except OSError:
            pass

    def start(self, workspace: Workspace, command: list[str], log_name: str = "harness.log", **kwargs: Any) -> HarnessRun:
        run = HarnessRun(
            self.adapter, workspace.root, self.tmp / "home", self.stack.model_server, self.logs / log_name, command, **kwargs
        )
        self.addCleanup(run.close)
        return run

    def assert_seen(self, needle: str, text: str, why: str) -> None:
        # The text is a whole conversation: say what was missing, and show
        # only the end of what was there.
        self.assertTrue(needle in text, f"{why}: {needle!r} not found in (end of) {excerpt(text)!r}")
