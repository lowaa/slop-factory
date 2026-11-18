from __future__ import annotations

import contextlib
import io
import logging
import os
import tempfile
import unittest
from pathlib import Path

from slop_haus_supervisor.harness.text import SecretMasker
from slop_haus_supervisor.service import log_setup


class LogSetupTest(unittest.TestCase):
    def setUp(self) -> None:
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.tmp = Path(tmp.name)
        self.log = logging.getLogger("test-log-setup")

    def test_the_file_rotates_at_the_size_limit_and_keeps_old_files(self) -> None:
        path = self.tmp / "s.log"
        old_umask = os.umask(0o022)
        self.addCleanup(os.umask, old_umask)

        with log_setup.configured(SecretMasker(), path, max_bytes=500):
            for i in range(40):
                self.log.info("line %d %s", i, "x" * 40)

        rotated = sorted(p.name for p in self.tmp.iterdir())
        self.assertIn("s.log.1", rotated)
        self.assertLessEqual(len(rotated), log_setup.LOG_BACKUPS + 1)
        self.assertLess(path.stat().st_size, 600)
        self.assertEqual({0o600}, {p.stat().st_mode & 0o777 for p in self.tmp.iterdir()})

    def test_errors_go_to_stderr_and_the_rest_to_stdout(self) -> None:
        out, err = io.StringIO(), io.StringIO()

        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err), log_setup.configured(SecretMasker()):
            self.log.info("fine")
            self.log.error("broken")

        self.assertIn("fine", out.getvalue())
        self.assertNotIn("broken", out.getvalue())
        self.assertIn("broken", err.getvalue())
        self.assertNotIn("fine", err.getvalue())

    def test_a_secret_in_a_traceback_is_masked_and_the_handlers_are_removed_after(self) -> None:
        path = self.tmp / "s.log"
        handlers = list(logging.getLogger().handlers)

        with log_setup.configured(SecretMasker(["hunter2-secret"]), path):
            try:
                raise RuntimeError("bad token hunter2-secret")
            except RuntimeError:
                self.log.exception("failed with hunter2-secret")

        text = path.read_text()
        self.assertNotIn("hunter2-secret", text)
        self.assertIn("bad token ***", text)
        self.assertEqual(handlers, logging.getLogger().handlers)
