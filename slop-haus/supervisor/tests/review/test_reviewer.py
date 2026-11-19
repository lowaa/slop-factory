"""The reviewer against real git, a local bare origin and a fake review.sh: the commands are the behaviour."""

from __future__ import annotations

import json
import logging
import os
import select
import subprocess
import tempfile
import time
import unittest
from collections.abc import Callable, Mapping
from dataclasses import replace
from pathlib import Path
from typing import Any, TextIO
from unittest import mock

from slop_haus_supervisor.review.checkout import canonical_remote
from slop_haus_supervisor.fleet.policy import review_prompt
from slop_haus_supervisor.harness.codex_login import CodexLogin, LoginRefreshError
from slop_haus_supervisor.review.logins import ReviewLogins
from slop_haus_supervisor.review.result import PERSONA, ReviewOutcome, ReviewResult
from slop_haus_supervisor.review.reviewer import ReviewRun, Reviewer, ReviewJob, ReviewSettings, review_environment
from slop_haus_supervisor.slophaus.client import ReviewRequest
from fakes import FakeClock
from process_state import process_running

BRANCH = "ticket-7-a1"
WAIT = 120.0
REMOTE = "example.test/acme/app"
URL = f"https://{REMOTE}.git"

# Records what it was run with, then acts as FAKE_MODE says: ok (the default), fail, garbage or block.
FAKE_REVIEW = r"""#!/usr/bin/env bash
set -u
args="$*"
persona=""
intent=""
base=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --persona=*) persona="${1#--persona=}" ;;
    --intent) intent="$2"; shift ;;
    --from-commit-hash=*) base="${1#--from-commit-hash=}" ;;
  esac
  shift
done
rec="$FAKE_RECORD/$persona"
mkdir -p "$rec"
# One line per process started, so a second run shows.
printf '%s\n' "$args" >> "$rec/argv"
pwd > "$rec/cwd"
git rev-parse --abbrev-ref HEAD > "$rec/branch"
git rev-parse HEAD > "$rec/head"
printf '%s' "$base" > "$rec/base"
cat "$intent" > "$rec/intent"
cat .env > "$rec/dotenv" 2>/dev/null || true
git status --porcelain > "$rec/status"
printf '%s\n%s\n%s\n%s\n' "${CLAUDE_CONFIG_DIR:-}" "${CODEX_HOME:-}" "${PI_CODING_AGENT_DIR:-}" "${HOME:-}" > "$rec/logins"
cat "${CODEX_HOME:-/nonexistent}/auth.json" > "$rec/codex_auth" 2>/dev/null || true
printf '%s\n%s\n%s\n' "${SLOP_REVIEW_REPORT_URL:-}" "${SLOP_REVIEW_REPORT_TOKEN_COMMAND:-}" "${SLOP_REVIEW_REPORT_TOKEN:-unset}" > "$rec/reporting"
case "${FAKE_MODE:-ok}" in
  fail) echo "slop-review: provider failed for $persona" >&2; exit 3 ;;
  garbage) echo "not json" ;;
  block) sleep 600 & echo "$persona" > "$FAKE_FIFO"; wait; exit 1 ;;
  *) printf '{"review": {"verdict": "request_changes", "findings": [{"file": "x"}]}, "report": {"run_id": "r-%s"}}\n' "$persona" ;;
esac
"""


def git(cwd: Path, *args: str) -> str:
    env = ["-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"]
    return subprocess.run(["git", *env, *args], cwd=cwd, check=True, capture_output=True, text=True).stdout.strip()


class ReviewerTest(unittest.TestCase):
    def setUp(self) -> None:
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.root = Path(tmp.name)
        origin = self.root / "origin.git"
        self.slot = self.root / "slot"
        git(self.root, "init", "--bare", "-b", "main", str(origin))
        # The manifest's https remote, served by the local bare repo.
        environ = mock.patch.dict(os.environ, {"GIT_CONFIG_COUNT": "1", "GIT_CONFIG_KEY_0": f"url.{origin}.insteadOf", "GIT_CONFIG_VALUE_0": URL})
        environ.start()
        self.addCleanup(environ.stop)
        git(self.root, "clone", URL, str(self.slot))
        (self.slot / "a.txt").write_text("one\n")
        git(self.slot, "add", "a.txt")
        git(self.slot, "commit", "-m", "init")
        git(self.slot, "push", "origin", "HEAD:main")
        self.base = git(self.slot, "rev-parse", "HEAD")
        git(self.slot, "switch", "-c", BRANCH)
        (self.slot / "a.txt").write_text("two\n")
        git(self.slot, "commit", "-am", "the change")
        git(self.slot, "push", "origin", BRANCH)
        self.head = git(self.slot, "rev-parse", "HEAD")

        self.script = self.root / "slop-review" / "review.sh"
        self.script.parent.mkdir()
        self.script.write_text(FAKE_REVIEW)
        self.script.chmod(0o755)
        self.token = self.root / "supervisor" / ".slop-haus" / "token.sh"
        self.token.parent.mkdir(parents=True)
        self.token.write_text("#!/bin/sh\necho t\n")
        self.state = self.root / "state" / "reviews"
        self.record = self.root / "record"
        self.record.mkdir()
        self.fifo = self.root / "started.fifo"
        os.mkfifo(self.fifo)
        self.env: dict[str, str] = {
            "PATH": os.environ["PATH"], "HOME": str(self.root), "FAKE_RECORD": str(self.record), "FAKE_FIFO": str(self.fifo),
            "GIT_CONFIG_NOSYSTEM": "1", "SLOP_REVIEW_REPORT_TOKEN": "an agent's own token",
            "CLAUDE_CODE_OAUTH_TOKEN": "a-tenant-login-0001",
        }
        self._fifo: TextIO | None = None
        self.supervisor_root = self.root / "tenant"
        self.settings = ReviewSettings(self.script, self.state, "https://slop.example", self.token, ReviewLogins.under(self.supervisor_root))

    def reviewer(
        self, settings: ReviewSettings | None = None, repo_env: str | None = "OPENROUTER_API_KEY=k\n", remotes: list[str] | None = None
    ) -> Reviewer:
        env: Mapping[str, str] = self.env
        listed = remotes if remotes is not None else [REMOTE]
        return Reviewer(settings or self.settings, lambda _agent: env, lambda _agent: repo_env, lambda: listed)

    def job(self, head: str | None = None, branch: str = BRANCH) -> ReviewJob:
        return ReviewJob("agent-1", self.slot, branch, ReviewRequest(head or self.head, "Makes it two.", "2026-11-18T10:00:00Z"))

    def review(self, reviewer: Reviewer | None = None, job: ReviewJob | None = None) -> ReviewResult:
        result = (reviewer or self.reviewer()).start(job or self.job()).wait(WAIT)
        assert result is not None, "the review did not end"
        return result

    def recorded(self, what: str) -> str:
        return (self.record / PERSONA / what).read_text()

    def test_one_process_reviews_the_pushed_head_on_the_attempt_branch_and_the_directory_goes(self) -> None:
        result = self.review()

        self.assertEqual(ReviewOutcome.REVIEWED, result.outcome)
        assert result.run is not None
        self.assertEqual(("request_changes", 1, True), (result.run.verdict, result.run.findings, result.run.reported))
        self.assertEqual([PERSONA], [p.name for p in self.record.iterdir()], "the one persona, and no other, ran")
        self.assertEqual(
            [f"--persona={PERSONA} --intent {self.state}"], [line.split("/agent-1/")[0] for line in self.recorded("argv").splitlines()],
            "exactly one process",
        )
        self.assertIn(f"--from-commit-hash={self.base}", self.recorded("argv"))
        self.assertEqual(BRANCH + "\n", self.recorded("branch"), "slop-review files the run under the branch's name")
        self.assertEqual(self.head + "\n", self.recorded("head"))
        self.assertEqual(self.base, self.recorded("base"), "diffed from the merge-base with origin's main")
        self.assertEqual("Makes it two.", self.recorded("intent"))
        self.assertEqual("OPENROUTER_API_KEY=k\n", self.recorded("dotenv"))
        self.assertEqual("", self.recorded("status"), "the installed .env is not part of the change")
        # The script records its physical cwd; macOS temp dirs sit under a /var symlink.
        self.assertTrue(os.path.realpath(self.recorded("cwd").strip()).startswith(os.path.realpath(self.state / "agent-1")), "never the agent's checkout")
        self.assertEqual(
            f"https://slop.example\n{self.token}\nunset\n", self.recorded("reporting"),
            "posted to the server with the supervisor's token command; an inherited token would win over it",
        )
        self.assertEqual([], self.left())

    def test_a_head_that_is_only_local_is_not_reviewed(self) -> None:
        # The agent's commits are on the slot's disk; only origin's branch says what was pushed.
        (self.slot / "a.txt").write_text("three\n")
        git(self.slot, "commit", "-am", "not pushed")
        local = git(self.slot, "rev-parse", "HEAD")

        result = self.review(job=self.job(local))

        self.assertEqual(ReviewOutcome.NOT_PUSHED, result.outcome)
        self.assertEqual([], list(self.record.iterdir()), "nothing ran, so nothing was posted")
        self.assertEqual([], self.left())

    def test_a_head_pushed_to_another_branch_is_not_this_attempts(self) -> None:
        git(self.slot, "switch", "-c", "other")
        (self.slot / "a.txt").write_text("elsewhere\n")
        git(self.slot, "commit", "-am", "on another branch")
        # On origin twice over, and on main, but not on this attempt's branch.
        git(self.slot, "push", "origin", "other", "HEAD:main")
        elsewhere = git(self.slot, "rev-parse", "HEAD")
        self.assertEqual(ReviewOutcome.NOT_PUSHED, self.review(job=self.job(elsewhere)).outcome)

    def test_an_unknown_head_is_not_pushed(self) -> None:
        self.assertEqual(ReviewOutcome.NOT_PUSHED, self.review(job=self.job("b" * 40)).outcome)

    def test_a_head_that_is_not_a_sha_is_refused_before_git_sees_it(self) -> None:
        result = self.review(job=self.job("--upload-pack=evil"))
        self.assertEqual(ReviewOutcome.FAILED, result.outcome)
        self.assertIn("not a full commit sha", result.detail)

    def test_a_branch_that_is_not_a_branch_name_is_refused(self) -> None:
        for branch in ("--upload-pack=evil", "a..b"):
            result = self.review(job=self.job(branch=branch))
            self.assertEqual(ReviewOutcome.FAILED, result.outcome, branch)
            self.assertIn("is not a branch name", result.detail)

    def test_a_ticket_without_a_branch_is_not_reviewed(self) -> None:
        result = self.review(job=self.job(branch=""))
        self.assertEqual(ReviewOutcome.FAILED, result.outcome)
        self.assertEqual([], list(self.record.iterdir()))

    def test_an_origin_the_agent_pointed_elsewhere_is_not_cloned(self) -> None:
        # A repo of the agent's own could serve the head with a main equal to it: an empty diff, approved.
        fake = self.root / "fake.git"
        git(self.root, "clone", "--bare", str(self.root / "origin.git"), str(fake))
        for url in (str(fake), f"file://{fake}", "https://evil.example/acme/app.git", f"https://evil.example/x/{REMOTE}.git"):
            git(self.slot, "remote", "set-url", "origin", url)
            result = self.review()
            self.assertEqual(ReviewOutcome.FAILED, result.outcome, url)
            self.assertIn("is not a repo of the workspace manifest", result.detail)
        self.assertEqual([], list(self.record.iterdir()))

    def test_the_clone_comes_from_the_manifest_remote_not_the_slots_url(self) -> None:
        # Same repo by host and path, but the agent's URL carries a user and a trailing slash the clone does not use.
        git(self.slot, "remote", "set-url", "origin", f"https://someone@{REMOTE}/")
        self.assertEqual(ReviewOutcome.REVIEWED, self.review().outcome)

    def test_an_unreadable_manifest_reviews_nothing(self) -> None:
        reviewer = Reviewer(self.settings, lambda _agent: self.env, lambda _agent: None, lambda: None)
        result = self.review(reviewer)
        self.assertEqual(ReviewOutcome.FAILED, result.outcome)
        self.assertIn("manifest could not be read", result.detail)
        self.assertEqual([], list(self.record.iterdir()))

    def test_without_a_main_on_origin_the_review_picks_its_own_base(self) -> None:
        git(self.root / "origin.git", "symbolic-ref", "HEAD", f"refs/heads/{BRANCH}")
        git(self.root / "origin.git", "update-ref", "-d", "refs/heads/main")
        self.assertEqual(ReviewOutcome.REVIEWED, self.review().outcome)
        self.assertEqual("", self.recorded("base"), "no --from-commit-hash")

    def test_the_run_is_under_the_supervisors_logins_and_the_hosts_home(self) -> None:
        self.assertEqual(ReviewOutcome.REVIEWED, self.review().outcome)
        harness = self.supervisor_root / "harness"
        claude, codex, pi, home = self.recorded("logins").splitlines()
        self.assertEqual(str(harness / "claude"), claude)
        self.assertEqual(str(harness / "pi"), pi)
        self.assertTrue(codex.startswith(str(self.state / "agent-1")), "Codex's is the review's own, gone with it")
        self.assertEqual(self.env["HOME"], home, "slop-review's sandbox denies the credential stores under the real HOME")

    def codex_review(self, post: Callable[[str, Mapping[str, str]], Mapping[str, Any]]) -> tuple[Path, ReviewResult]:
        """A codex review whose master login is due a refresh, refreshed through `post`."""
        master = self.supervisor_root / "harness" / "codex"
        master.mkdir(parents=True)
        tokens = {"refresh_token": "master-refresh"}
        (master / "auth.json").write_text(json.dumps({"auth_mode": "chatgpt", "tokens": tokens, "last_refresh": "1970-01-01T00:00:00+00:00"}))
        login = CodexLogin(master, FakeClock(), logging.getLogger("test"), post=post)
        self.env["SLOP_REVIEW_PROVIDER"] = "codex"
        return master, self.review(self.reviewer(replace(self.settings, logins=ReviewLogins.under(self.supervisor_root, login))))

    def test_a_codex_review_runs_on_a_refreshed_copy_of_the_shared_login_that_goes_with_it(self) -> None:
        master, result = self.codex_review(lambda _url, _body: {"refresh_token": "rotated-refresh"})

        self.assertEqual(ReviewOutcome.REVIEWED, result.outcome)
        codex_home = self.recorded("logins").splitlines()[1]
        self.assertNotEqual(str(master), codex_home)
        self.assertEqual("rotated-refresh", json.loads(self.recorded("codex_auth"))["tokens"]["refresh_token"])
        self.assertFalse(Path(codex_home).exists())
        self.assertEqual([], self.left())

    def test_a_codex_login_that_cannot_be_refreshed_fails_the_review_before_the_run_starts(self) -> None:
        def refused(_url: str, _body: Mapping[str, str]) -> Mapping[str, Any]:
            raise LoginRefreshError("the token endpoint answered 401")

        _, result = self.codex_review(refused)

        self.assertEqual(ReviewOutcome.FAILED, result.outcome)
        self.assertEqual("the Codex login could not be refreshed: the token endpoint answered 401", result.detail)
        self.assertEqual([], list(self.record.iterdir()))
        self.assertEqual([], self.left())

    def test_a_provider_without_its_scoped_login_reviews_nothing_and_says_how_to_fix_it(self) -> None:
        del self.env["CLAUDE_CODE_OAUTH_TOKEN"]

        result = self.review()

        self.assertEqual(ReviewOutcome.NO_REVIEWER, result.outcome)
        prompt = review_prompt(result, BRANCH)
        self.assertIn("The supervisor cannot review on this host: the claude provider needs CLAUDE_CODE_OAUTH_TOKEN", prompt)
        self.assertIn("`slop-haus harness login claude`", prompt)
        self.assertEqual([], list(self.record.iterdir()), "nothing ran, so nothing was posted")
        self.assertEqual([], self.left())

    def test_a_host_without_a_usable_reviewer_runs_nothing(self) -> None:
        not_executable = self.root / "plain.sh"
        not_executable.write_text("")
        cases = {
            "is not set": replace(self.settings, script=None),
            "absolute path": replace(self.settings, script=Path("review.sh")),
            "not an executable file": replace(self.settings, script=not_executable),
            "not signed in": replace(self.settings, token_command=self.root / "missing" / "token.sh"),
        }
        for why, settings in cases.items():
            result = self.review(self.reviewer(settings))
            self.assertEqual(ReviewOutcome.NO_REVIEWER, result.outcome, why)
            self.assertIn(why, result.detail)
        self.assertEqual([], list(self.record.iterdir()))
        self.assertEqual([], self.left())

    def test_a_failed_or_garbled_run_is_reported(self) -> None:
        for mode, error in (("fail", f"exit 3: slop-review: provider failed for {PERSONA}"), ("garbage", "its output is not JSON")):
            self.env["FAKE_MODE"] = mode
            result = self.review()
            self.assertEqual(ReviewOutcome.REVIEWED, result.outcome, mode)
            assert result.run is not None
            self.assertEqual(error, result.run.error, mode)
            self.assertEqual([], self.left())

    def test_a_clone_that_fails_is_a_failure_and_leaves_nothing(self) -> None:
        (self.root / "origin.git").rename(self.root / "gone.git")
        result = self.review()
        self.assertEqual(ReviewOutcome.FAILED, result.outcome)
        self.assertIn("could not clone origin", result.detail)
        self.assertEqual([], self.left())

    def test_what_a_crash_left_is_cleared_at_start(self) -> None:
        (self.state / "agent-1" / "tmpold" / "checkout").mkdir(parents=True)
        self.reviewer().clear_leftovers()
        self.assertFalse(self.state.exists())
        self.assertEqual(ReviewOutcome.REVIEWED, self.review().outcome)

    def test_a_kill_during_the_clone_ends_it_and_removes_the_directory(self) -> None:
        # A git that announces a clone and then hangs, as one stuck on the network would; every other command is git's.
        real = subprocess.run(["sh", "-c", "command -v git"], capture_output=True, text=True, check=True).stdout.strip()
        bin_dir = self.root / "bin"
        bin_dir.mkdir()
        (bin_dir / "git").write_text(
            f'#!/bin/sh\nfor a in "$@"; do [ "$a" = clone ] && {{ sleep 600 & echo clone > "{self.fifo}"; wait; exit 1; }}; done\nexec "{real}" "$@"\n'
        )
        (bin_dir / "git").chmod(0o755)
        with mock.patch.dict(os.environ, {"PATH": f"{bin_dir}:{os.environ['PATH']}"}):
            handle = self.reviewer().start(self.job())
            self.assertEqual("clone", self.next_started(handle))
            handle.kill()
        result = handle.wait(0)
        self.assertEqual(ReviewOutcome.KILLED, result.outcome if result else None)
        self.assertEqual([], self.left())
        self.assertEqual([], [pid for pid in self._sleepers() if self._alive(pid)])

    def test_a_kill_ends_the_run_and_removes_the_directory(self) -> None:
        self.env["FAKE_MODE"] = "block"
        handle = self.reviewer().start(self.job())
        self.assertEqual(PERSONA, self.next_started(handle))
        self.assertIsNone(handle.wait(0))
        sleepers = self._await_sleepers(1)

        handle.kill()

        result = handle.wait(0)
        assert result is not None
        self.assertEqual(ReviewOutcome.KILLED, result.outcome)
        self.assertEqual([], self.left())
        self.assertEqual([], [pid for pid in sleepers if self._alive(pid)])

    def left(self) -> list[Path]:
        """What reviews left in the agent's directory, which outlives them."""
        agent = self.state / "agent-1"
        return list(agent.iterdir()) if agent.exists() else []

    def next_started(self, handle: ReviewRun) -> str:
        """The next run to block; fails the test instead of hanging when the review ended before any could."""
        if self._fifo is None:
            # Read-write, so the test is itself a writer and a read waits for the next line instead of seeing EOF.
            self._fifo = os.fdopen(os.open(self.fifo, os.O_RDWR), "r")
            self.addCleanup(self._fifo.close)
        while not select.select([self._fifo], [], [], 1.0)[0]:
            ended = handle.wait(0)
            if ended is not None:
                self.fail(f"the review ended before its run blocked: {ended}")
        return self._fifo.readline().strip()

    def _await_sleepers(self, count: int) -> list[int]:
        """The run's sleepers once there are `count`: the fake forks `sleep` before it signals, but the child is
        still a copy of the shell until it execs, so it may not show yet. A deadline, not a fixed wait."""
        deadline = time.monotonic() + 10
        while True:
            found = self._sleepers()
            if len(found) >= count or time.monotonic() > deadline:
                self.assertEqual(count, len(found), "the fake's sleeper never appeared")
                return found
            time.sleep(0.01)

    def _sleepers(self) -> list[int]:
        """This test's `sleep 600` processes, whose cwd is in the root: the image the tests run in has no pgrep."""
        if not Path("/proc/self").is_dir():
            return self._sleepers_without_proc()
        mine: list[int] = []
        for proc in Path("/proc").iterdir():
            if not proc.name.isdigit():
                continue
            try:
                if (proc / "cmdline").read_bytes().split(b"\0")[:2] != [b"sleep", b"600"]:
                    continue
                if (proc / "cwd").resolve().is_relative_to(self.root):
                    mine.append(int(proc.name))
            except OSError:
                continue
        return mine

    def _sleepers_without_proc(self) -> list[int]:
        """macOS: ps for the command, lsof for the cwd."""
        listing = subprocess.run(["ps", "-ax", "-o", "pid=,command="], capture_output=True, text=True, check=True).stdout
        mine: list[int] = []
        for line in listing.splitlines():
            pid, _, command = line.strip().partition(" ")
            if command.split()[:2] not in (["sleep", "600"], ["/bin/sleep", "600"]):
                continue
            # The sandbox-less lsof prints `n<cwd>` for the process, and nothing when it has gone since the listing.
            cwd = subprocess.run(["lsof", "-a", "-p", pid, "-d", "cwd", "-Fn"], capture_output=True, text=True, check=False).stdout
            if any(Path(n[1:]).is_relative_to(self.root.resolve()) for n in cwd.splitlines() if n.startswith("n/")):
                mine.append(int(pid))
        return mine

    @staticmethod
    def _alive(pid: int) -> bool:
        return process_running(pid)


class CanonicalRemoteTest(unittest.TestCase):
    def test_network_urls_name_their_host_and_path(self) -> None:
        for url in (
            "https://github.com/acme/app.git", "https://token@GitHub.com/acme/app", "git@github.com:acme/app.git",
            "ssh://git@github.com/acme/app.git", "ssh://git@github.com:22/acme/app", "https://github.com/acme/app/",
        ):
            self.assertEqual("github.com/acme/app", canonical_remote(url), url)

    def test_anything_else_has_none(self) -> None:
        for url in ("/srv/app.git", "file:///srv/app.git", "ext::sh -c evil", "https://github.com/../x", "github.com/acme/app", ""):
            self.assertIsNone(canonical_remote(url), url)


class ReviewEnvironmentTest(unittest.TestCase):
    def test_reporting_is_the_supervisors_whatever_the_base_says(self) -> None:
        settings = ReviewSettings(
            Path("/r/review.sh"), Path("/s"), "https://slop.example", Path("/root/supervisor/.slop-haus/token.sh"), ReviewLogins.under(Path("/root"))
        )
        base = {
            "SLOP_REVIEW_REPORT_URL": "https://elsewhere",
            "SLOP_REVIEW_REPORT_TOKEN": "agent-token",
            "SLOP_REVIEW_REPORT_TOKEN_COMMAND": "/slots/a/.slop-haus/token.sh",
            "OPENROUTER_API_KEY": "k",
        }
        self.assertEqual(
            {
                "SLOP_REVIEW_REPORT_URL": "https://slop.example",
                "SLOP_REVIEW_REPORT_TOKEN_COMMAND": "/root/supervisor/.slop-haus/token.sh",
                "OPENROUTER_API_KEY": "k",
            },
            review_environment(settings, base),
        )


if __name__ == "__main__":
    unittest.main()

