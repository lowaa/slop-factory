"""Runs slop-review on an agent's pushed head, off the slot's thread, and posts the run as the supervisor.

One review per slot at a time: one slop-review pass over the whole change. The slot waits on the handle as it
waits on a harness run, and kills it on a revoked lease, its time budget or a stop. Whatever ends it, the review's
directory under `<root>/state/reviews/` is gone before the result is published.
"""

from __future__ import annotations

import os
import shutil
import subprocess
import tempfile
import threading
from collections.abc import Callable, Mapping, Sequence
from dataclasses import dataclass, field
from pathlib import Path
from typing import Protocol

from slop_haus_supervisor.harness.codex_login import LoginRefreshError
from slop_haus_supervisor.review.checkout import CheckoutFailed, NotPushed, ReviewCheckout
from slop_haus_supervisor.review.logins import ReviewLogins
from slop_haus_supervisor.review.processes import ProcessGroup, ReviewKilled
from slop_haus_supervisor.review.result import PERSONA, ReviewOutcome, ReviewResult, RunResult, parse_run
from slop_haus_supervisor.service.run_env import REVIEW_SCRIPT
from slop_haus_supervisor.slophaus.client import ReviewRequest
from slop_haus_supervisor.workspace import config as cfg
from slop_haus_supervisor.workspace import envfile

# Names that would send the runs somewhere else or under another credential: an explicit token beats the command.
_REPORTING = ("SLOP_REVIEW_REPORT_URL", "SLOP_REVIEW_REPORT_TOKEN", "SLOP_REVIEW_REPORT_TOKEN_COMMAND")
# How long a kill waits for the review's thread to remove its directory; the processes are already signalled.
KILL_JOIN = 60.0


@dataclass(frozen=True)
class ReviewJob:
    agent: str
    # The slot's repo, read only for which manifest repo it is a checkout of; nothing else is taken from it.
    repo_dir: Path
    branch: str
    request: ReviewRequest


@dataclass(frozen=True)
class ReviewSettings:
    script: Path | None
    # <root>/state/reviews: each agent's review gets its own directory under it.
    state_dir: Path
    report_url: str
    # The supervisor's own token.sh, never a slot's: the server accepts the runs from this supervisor only.
    token_command: Path
    logins: ReviewLogins
    persona: str = PERSONA
    git_timeout: float = 600.0


class ReviewRun(Protocol):
    def wait(self, timeout: float) -> ReviewResult | None:
        """The result once the review has ended and its directory is gone, or None after `timeout`."""
        ...

    def kill(self) -> None:
        """Ends the review's processes and waits (bounded) for its directory to be removed. Safe from any thread."""
        ...


class Reviews(Protocol):
    def start(self, job: ReviewJob) -> ReviewRun: ...


def review_script(environ: Mapping[str, str]) -> Path | None:
    value = environ.get(REVIEW_SCRIPT, "").strip()
    return Path(value) if value else None


def reviewer_missing(settings: ReviewSettings) -> str:
    """Why this host cannot review, or "" when it can."""
    script = settings.script
    if script is None:
        return f"{REVIEW_SCRIPT} is not set in run.env"
    if not script.is_absolute():
        return f"{REVIEW_SCRIPT} must be an absolute path, not {str(script)[:200]!r}"
    if not script.is_file() or not os.access(script, os.X_OK):
        return f"{REVIEW_SCRIPT} names {str(script)[:200]}, which is not an executable file"
    if not settings.token_command.is_file():
        return "the supervisor is not signed in (`slop-haus login`), so it has no token to post reviews with"
    return ""


class Reviewer:
    def __init__(
        self,
        settings: ReviewSettings,
        env_of: Callable[[str], Mapping[str, str]],
        repo_env_of: Callable[[str], str | None],
        remotes: Callable[[], Sequence[str] | None],
    ) -> None:
        """`env_of` is the review's environment for an agent's change (its harness login and repo.env values);
        `repo_env_of` is the `.env` text a checkout of the agent's gets; `remotes` is the workspace manifest's
        repos (host/path), None when it cannot be read, and is asked on the review's thread."""
        self._settings = settings
        self._env_of = env_of
        self._repo_env_of = repo_env_of
        self._remotes = remotes

    def missing(self) -> str:
        return reviewer_missing(self._settings)

    def clear_leftovers(self) -> None:
        """Removes what reviews a crashed supervisor ran left behind; called at start, before any review runs."""
        shutil.rmtree(self._settings.state_dir, ignore_errors=True)

    def start(self, job: ReviewJob) -> ReviewRun:
        # Read here, on the slot's thread, so an unreadable env file fails the start the slot reports.
        env = review_environment(self._settings, self._env_of(job.agent))
        return ReviewHandle(self._settings, job, env, self._repo_env_of(job.agent), self._remotes)


def review_environment(settings: ReviewSettings, base: Mapping[str, str]) -> dict[str, str]:
    """`base` with the runs posted to this supervisor's server under its own token, whatever the repo's .env says."""
    env = {k: v for k, v in base.items() if k not in _REPORTING}
    env["SLOP_REVIEW_REPORT_URL"] = settings.report_url
    env["SLOP_REVIEW_REPORT_TOKEN_COMMAND"] = str(settings.token_command)
    return env


@dataclass
class _Paths:
    root: Path
    checkout: Path = field(init=False)
    intent: Path = field(init=False)
    out: Path = field(init=False)
    err: Path = field(init=False)

    def __post_init__(self) -> None:
        self.checkout = self.root / "checkout"
        self.intent = self.root / "intent.md"
        self.out = self.root / "review.out"
        self.err = self.root / "review.err"


class ReviewHandle:
    def __init__(
        self,
        settings: ReviewSettings,
        job: ReviewJob,
        env: Mapping[str, str],
        repo_env: str | None,
        remotes: Callable[[], Sequence[str] | None],
    ) -> None:
        self._settings = settings
        self._job = job
        self._env = env
        self._repo_env = repo_env
        self._remotes = remotes
        self._agent_dir = settings.state_dir / cfg.check_name("agent", job.agent)
        self._paths: _Paths | None = None
        self._procs = ProcessGroup()
        self._done = threading.Event()
        self._result: ReviewResult | None = None
        self._thread = threading.Thread(target=self._main, name=f"review-{job.agent}", daemon=True)
        self._thread.start()

    def wait(self, timeout: float) -> ReviewResult | None:
        self._done.wait(timeout)
        return self._result

    def kill(self) -> None:
        self._procs.kill()
        self._thread.join(KILL_JOIN)

    def _main(self) -> None:
        head = self._job.request.head_sha
        try:
            result = self._review()
        except ReviewKilled:
            result = ReviewResult(ReviewOutcome.KILLED, head)
        except NotPushed as err:
            result = ReviewResult(ReviewOutcome.NOT_PUSHED, head, detail=str(err))
        except CheckoutFailed as err:
            result = ReviewResult(ReviewOutcome.FAILED, head, detail=str(err))
        except LoginRefreshError as err:
            result = ReviewResult(ReviewOutcome.FAILED, head, detail=f"the Codex login could not be refreshed: {err}")
        except subprocess.TimeoutExpired:
            result = ReviewResult(ReviewOutcome.FAILED, head, detail="a git command passed its time limit")
        except Exception as err:  # noqa: BLE001 -- any failure is the agent's to hear about, not the slot's to raise
            result = ReviewResult(ReviewOutcome.FAILED, head, detail=f"{type(err).__name__}: {err}")
        finally:
            if self._paths is not None:
                shutil.rmtree(self._paths.root, ignore_errors=True)
        self._result = result
        self._done.set()

    def _review(self) -> ReviewResult:
        head = self._job.request.head_sha
        missing = reviewer_missing(self._settings)
        if missing:
            return ReviewResult(ReviewOutcome.NO_REVIEWER, head, detail=missing)
        # Before the clone: a provider without its scoped login is a host to set up, not a review to try.
        missing = self._settings.logins.missing(self._env, self._job.agent)
        if missing:
            return ReviewResult(ReviewOutcome.NO_REVIEWER, head, detail=missing)
        if not self._job.branch:
            return ReviewResult(ReviewOutcome.FAILED, head, detail="the ticket has no branch to file the review under")
        remotes = self._remotes()
        if remotes is None:
            return ReviewResult(ReviewOutcome.FAILED, head, detail="the workspace manifest could not be read, so which repo to clone is not known")
        self._agent_dir.mkdir(mode=0o700, parents=True, exist_ok=True)
        paths = self._paths = _Paths(Path(tempfile.mkdtemp(dir=self._agent_dir)))
        checkout = ReviewCheckout(self._procs, self._settings.git_timeout).prepare(
            self._job.repo_dir, self._job.branch, head, paths.checkout, remotes
        )
        # The repo's own slop-review settings (provider keys), as the agent's checkout has them.
        if self._repo_env is not None:
            envfile.write_private(checkout.directory / ".env", self._repo_env)
        envfile.write_private(paths.intent, self._job.request.intent)
        env = self._settings.logins.scoped(self._env, paths.root)
        proc: subprocess.Popen[bytes] | None = None
        try:
            proc = self._start_run(paths, checkout.base, checkout.directory, env)
            run = self._finish(paths, proc)
        except BaseException:
            # A kill, or a process that would not start: nothing the review started outlives it.
            self._procs.kill()
            if proc is not None:
                proc.wait()
            raise
        return ReviewResult(ReviewOutcome.REVIEWED, head, run=run)

    def _start_run(self, paths: _Paths, base: str | None, cwd: Path, env: Mapping[str, str]) -> subprocess.Popen[bytes]:
        assert self._settings.script is not None
        # --env-file= keeps the .env of the checkout review.sh lives in (the user's) out: the review's settings are
        # `env` alone, and a token in that file would beat the supervisor's token command.
        argv = [str(self._settings.script), "--env-file=", f"--persona={self._settings.persona}", "--intent", str(paths.intent)]
        if base is not None:
            argv.append(f"--from-commit-hash={base}")
        with paths.out.open("wb") as out, paths.err.open("wb") as err:
            return self._procs.start(argv, cwd, env, out, err)

    def _finish(self, paths: _Paths, proc: subprocess.Popen[bytes]) -> RunResult:
        code = self._procs.wait(proc)
        return parse_run(code, paths.out.read_text(errors="replace"), paths.err.read_text(errors="replace"))
