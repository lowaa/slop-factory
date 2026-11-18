from __future__ import annotations

import subprocess
import threading
import time
from collections.abc import Callable, Mapping
from pathlib import Path

from slop_haus_supervisor.ports import ProcessRunner


class TokenUnavailable(Exception):
    pass


# The helper hands back the stored access token while more than 60s of it remain, so a token
# fetched at most this long ago is still good for the request it is used on. Asking again after
# this is cheap (no network in the common case) and leaves token.sh the one owner of when to refresh.
MAX_AGE = 30.0
RUN_TIMEOUT = 90.0


class SlotToken:
    """The slot's agent token, from the `token.sh` its sign-in left in `.slop-haus/`.

    The helper decides when to refresh; this only avoids spawning it for every request in a burst.
    """

    def __init__(
        self,
        helper: Path,
        runner: ProcessRunner,
        *,
        max_age: float = MAX_AGE,
        monotonic: Callable[[], float] = time.monotonic,
        env: Mapping[str, str] | None = None,
    ) -> None:
        self._helper = helper
        self._runner = runner
        self._env = env
        self._max_age = max_age
        self._monotonic = monotonic
        self._lock = threading.Lock()
        self._token = ""
        self._fetched_at = 0.0

    def get(self, rejected: str = "") -> str:
        """A token to send. `rejected` is one the server just refused: it is replaced, once.

        Threads that were refused the same token together find the first one's replacement here
        instead of each running the helper.
        """
        with self._lock:
            stale = self._monotonic() - self._fetched_at >= self._max_age
            if not self._token or stale or (rejected and self._token == rejected):
                self._token = self._fetch()
                self._fetched_at = self._monotonic()
            return self._token

    def _fetch(self) -> str:
        try:
            done = self._runner.run([str(self._helper)], self._helper.parent, RUN_TIMEOUT, self._env)
        except (OSError, subprocess.TimeoutExpired) as err:
            raise TokenUnavailable(f"{self._helper}: {err}") from err
        token = done.stdout.strip()
        if done.returncode != 0 or not token:
            raise TokenUnavailable(f"{self._helper} gave no token: {done.stderr.strip()}")
        return token
