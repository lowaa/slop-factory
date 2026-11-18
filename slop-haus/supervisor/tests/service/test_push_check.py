from __future__ import annotations

from collections.abc import Mapping

import logging
import subprocess
import unittest
from pathlib import Path

from slop_haus_supervisor.ports import ProcessResult
from slop_haus_supervisor.service.push_check import PushCheck
from slop_haus_supervisor.workspace import config as cfg

LOG = logging.getLogger("test-push")


class Runner:
    def __init__(self, urls: dict[str, str], push: ProcessResult | BaseException) -> None:
        self.urls = urls
        self.push = push
        self.pushed: list[Path] = []
        self.push_argv: list[str] = []
        self.push_timeout: float | None = None

    def run(self, argv: list[str], cwd: Path, timeout: float | None = None, env: Mapping[str, str] | None = None) -> ProcessResult:
        if argv[1] == "remote":
            url = self.urls.get(cwd.name)
            return ProcessResult(0, url + "\n", "") if url else ProcessResult(128, "", "no remote")
        self.pushed.append(cwd)
        self.push_argv, self.push_timeout = argv, timeout
        if isinstance(self.push, BaseException):
            raise self.push
        return self.push


def dirs(*names: str) -> list[Path]:
    return [Path("/slots") / n for n in names]


class PushCheckTest(unittest.TestCase):
    def check(self, runner: Runner, *names: str) -> None:
        PushCheck(runner, LOG).check(dirs(*names))

    def test_a_push_that_works_passes_and_the_probe_is_a_dry_run_to_an_unused_ref(self) -> None:
        runner = Runner({"a": "git@h:o/r.git"}, ProcessResult(0, "", ""))

        self.check(runner, "a")

        self.assertEqual(["git", "push", "--dry-run", "origin", "HEAD:refs/heads/slop-haus-push-check"], runner.push_argv)
        self.assertEqual(30.0, runner.push_timeout)

    def test_a_refused_key_fails_with_the_fix_and_without_url_credentials(self) -> None:
        runner = Runner({"a": "https://user:s3cr3tpw@h/o/r.git"}, ProcessResult(128, "", "fatal: could not read Username for 'https://h': terminal prompts disabled"))

        with self.assertRaises(cfg.ConfigError) as caught:
            self.check(runner, "a")

        self.assertIn("ssh-agent", str(caught.exception))
        self.assertNotIn("s3cr3tpw", str(caught.exception))

    def test_permission_denied_over_ssh_fails(self) -> None:
        runner = Runner({"a": "git@h:o/r.git"}, ProcessResult(128, "", "git@h: Permission denied (publickey)."))

        with self.assertRaises(cfg.ConfigError):
            self.check(runner, "a")

    def test_an_unreachable_remote_only_warns(self) -> None:
        runner = Runner({"a": "git@h:o/r.git"}, ProcessResult(128, "", "ssh: Could not resolve hostname h"))

        with self.assertLogs(LOG, "WARNING"):
            self.check(runner, "a")

    def test_a_timeout_only_warns(self) -> None:
        runner = Runner({"a": "git@h:o/r.git"}, subprocess.TimeoutExpired("git", 30))

        with self.assertLogs(LOG, "WARNING"):
            self.check(runner, "a")

    def test_each_remote_is_probed_once(self) -> None:
        runner = Runner({"a": "git@h:o/r.git", "b": "git@h:o/r.git", "c": "git@h:o/other.git"}, ProcessResult(0, "", ""))

        self.check(runner, "a", "b", "c", "d")

        self.assertEqual(dirs("a", "c"), runner.pushed)
