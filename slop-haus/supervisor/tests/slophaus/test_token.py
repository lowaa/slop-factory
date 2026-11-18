from __future__ import annotations

import os
import shutil
import stat
import tempfile
import unittest
from pathlib import Path
from unittest import mock

from slop_haus_supervisor.ports import SubprocessRunner
from slop_haus_supervisor.slophaus.token import SlotToken, TokenUnavailable


class SlotTokenWithARealHelperTest(unittest.TestCase):
    def helper(self, body: str) -> Path:
        tmp = Path(tempfile.mkdtemp(prefix="slot-token-"))
        self.addCleanup(shutil.rmtree, tmp, ignore_errors=True)
        path = tmp / "token.sh"
        path.write_text("#!/bin/sh\n" + body)
        path.chmod(path.stat().st_mode | stat.S_IXUSR)
        return path

    def test_the_helper_is_run_against_the_given_server_not_the_shells(self) -> None:
        helper = self.helper('printf "%s" "$SLOP_HAUS_URL"')
        with mock.patch.dict(os.environ, {"SLOP_HAUS_URL": "https://some-other-env"}):
            token = SlotToken(helper, SubprocessRunner(), env={"SLOP_HAUS_URL": "https://mine"})
            self.assertEqual("https://mine", token.get())

    def test_a_rejected_token_is_replaced_once_and_the_replacement_is_shared(self) -> None:
        count = Path(tempfile.mkdtemp(prefix="slot-token-count-")) / "n"
        self.addCleanup(shutil.rmtree, count.parent, ignore_errors=True)
        helper = self.helper(f'echo x >> {count}; printf "t%s" "$(wc -l < {count} | tr -d " ")"')
        token = SlotToken(helper, SubprocessRunner())
        self.assertEqual("t1", token.get())
        self.assertEqual("t1", token.get(), "kept")
        self.assertEqual("t2", token.get(rejected="t1"))
        self.assertEqual("t2", token.get(rejected="t1"), "a second caller refused the same token finds the replacement")

    def test_a_token_older_than_max_age_is_fetched_again(self) -> None:
        count = Path(tempfile.mkdtemp(prefix="slot-token-count-")) / "n"
        self.addCleanup(shutil.rmtree, count.parent, ignore_errors=True)
        helper = self.helper(f'echo x >> {count}; printf "t%s" "$(wc -l < {count} | tr -d " ")"')
        now = [0.0]
        token = SlotToken(helper, SubprocessRunner(), max_age=30.0, monotonic=lambda: now[0])
        self.assertEqual("t1", token.get())
        now[0] = 29.0
        self.assertEqual("t1", token.get())
        now[0] = 31.0
        self.assertEqual("t2", token.get())

    def test_a_failing_helper_is_an_error_not_an_empty_token(self) -> None:
        for body in ("echo 'sign-in expired' >&2; exit 1", "exit 0"):
            with self.assertRaises(TokenUnavailable):
                SlotToken(self.helper(body), SubprocessRunner()).get()

    def test_a_helper_that_cannot_run_is_an_error(self) -> None:
        with self.assertRaises(TokenUnavailable):
            SlotToken(Path("/nonexistent/token.sh"), SubprocessRunner()).get()


if __name__ == "__main__":
    unittest.main()
