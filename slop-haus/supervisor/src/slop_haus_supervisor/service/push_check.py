from __future__ import annotations

import logging
import subprocess
from collections.abc import Iterable
from pathlib import Path

from slop_haus_supervisor.ports import ProcessRunner
from slop_haus_supervisor.workspace import config as cfg

# A ref nothing uses: the push is a dry run, so it is never created; it only has to reach the
# remote's receive side, which is where a missing key or an ssh-agent that is not there fails.
PROBE_REF = "refs/heads/slop-haus-push-check"
# What git and ssh say when they would have asked a person, or were refused.
AUTH_FAILURES = (
    "permission denied",
    "host key verification failed",
    "terminal prompts disabled",
    "could not read username",
    "authentication failed",
)
FIX = (
    "workers push without a person at the keyboard: use a key without a passphrase, or an ssh-agent socket the "
    "launcher provides (SSH_AUTH_SOCK in run.env); on macOS `ssh-add --apple-use-keychain` and "
    "`UseKeychain yes` in ~/.ssh/config; for https remotes, a credential helper with the token stored"
)


class PushCheck:
    def __init__(self, runner: ProcessRunner, logger: logging.Logger, timeout: float = 30.0) -> None:
        self._runner = runner
        self._log = logger
        self._timeout = timeout

    def check(self, slot_dirs: Iterable[Path]) -> None:
        """Fails when git cannot push from here without a prompt; a remote that cannot be reached only warns."""
        seen: set[str] = set()
        for directory in slot_dirs:
            url = self._runner.run(["git", "remote", "get-url", "origin"], directory)
            if url.returncode != 0 or url.stdout.strip() in seen:
                continue
            seen.add(url.stdout.strip())
            self._probe(directory, url.stdout.strip())

    def _probe(self, directory: Path, url: str) -> None:
        try:
            done = self._runner.run(["git", "push", "--dry-run", "origin", f"HEAD:{PROBE_REF}"], directory, self._timeout)
        except subprocess.TimeoutExpired:
            self._log.warning("push check of %s timed out after %.0fs; carrying on", directory, self._timeout)
            return
        if done.returncode == 0:
            return
        output = (done.stdout + done.stderr).lower()
        if any(marker in output for marker in AUTH_FAILURES):
            raise cfg.ConfigError(f"git cannot push to {_redacted(url)} from {directory} without a prompt. {FIX}")
        self._log.warning("push check of %s was inconclusive (exit %d); carrying on", directory, done.returncode)


def _redacted(url: str) -> str:
    scheme, sep, rest = url.partition("://")
    if sep and "@" in rest.split("/", 1)[0]:
        return f"{scheme}://***@{rest.split('@', 1)[1]}"
    return url
