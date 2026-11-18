"""One ChatGPT login for several Codex slots: a single refresher and per-slot copies of `auth.json`.

Providers rotate refresh tokens and treat a second use of a spent one as theft, revoking the whole family. Codex
processes that each refresh from the same login would therefore log every slot out. Here only `CodexLogin` ever
presents the refresh token, under a lock, and hands the result to the slots' copies; it refreshes well before Codex's
own staleness check, so Codex never finds a reason to. That last part is the unproven one.
Nothing here logs or returns the file's contents.
"""

from __future__ import annotations

import contextlib
import fcntl
import json
import logging
import os
import urllib.error
import urllib.request
from collections.abc import Callable, Generator, Mapping
from contextlib import AbstractContextManager
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

from slop_haus_supervisor.harness.text import as_dict
from slop_haus_supervisor.ports import Clock
from slop_haus_supervisor.service.run_env import CODEX_SHARED_LOGIN

# Codex treats a login older than 8 days as stale; refreshing at half of that keeps it from ever deciding to itself.
REFRESH_AFTER = 4 * 24 * 3600.0
# The public client id the Codex CLI logs in with.
CLIENT_ID = "app_EMoamEEZ73f0CkXaXp7hrann"
TOKEN_URL = "https://auth.openai.com/oauth/token"
REQUEST_TIMEOUT = 30.0

Locker = Callable[[Path], AbstractContextManager[None]]
Poster = Callable[[str, Mapping[str, str]], Mapping[str, Any]]


class LoginRefreshError(Exception):
    pass


@contextlib.contextmanager
def file_lock(path: Path) -> Generator[None]:
    """flock excludes other open file descriptions, so it holds between threads and between processes."""
    fd = os.open(path, os.O_RDWR | os.O_CREAT, 0o600)
    try:
        fcntl.flock(fd, fcntl.LOCK_EX)
        yield
    finally:
        os.close(fd)


def post_json(url: str, body: Mapping[str, str]) -> Mapping[str, Any]:
    request = urllib.request.Request(url, json.dumps(body).encode(), {"Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(request, timeout=REQUEST_TIMEOUT) as response:
            return as_dict(json.load(response)) or {}
    except urllib.error.HTTPError as err:
        # The body can echo a token; only the status leaves this function.
        raise LoginRefreshError(f"the token endpoint answered {err.code}") from err
    except (OSError, ValueError) as err:
        raise LoginRefreshError(f"the token endpoint could not be reached: {type(err).__name__}") from err


def write_private(path: Path, text: str) -> None:
    """Atomically, so a Codex reading its copy sees the old file or the new one, never half."""
    tmp = path.with_name(path.name + ".tmp")
    fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, "w", encoding="utf-8") as handle:
        handle.write(text)
    os.replace(tmp, path)


class CodexLogin:
    """The master login in `home`, its refresh, and the copies handed to slots."""

    def __init__(
        self,
        home: Path,
        clock: Clock,
        logger: logging.Logger,
        *,
        token_url: str = TOKEN_URL,
        refresh_after: float = REFRESH_AFTER,
        lock: Locker = file_lock,
        post: Poster = post_json,
    ) -> None:
        self._home = home
        self._clock = clock
        self._log = logger
        self._url = token_url
        self._after = refresh_after
        self._lock = lock
        self._post = post

    def refresh_if_due(self, copies: list[Path]) -> bool:
        """Refreshes when the login is old enough, then writes it to every copy. Whether it refreshed."""
        self._home.mkdir(mode=0o700, parents=True, exist_ok=True)
        with self._lock(self._home / "refresh.lock"):
            # Read under the lock: a caller that waited for another's refresh finds it done and presents nothing.
            auth = self._read()
            refreshed = self._due(auth)
            if refreshed:
                auth = self._refresh(auth)
                write_private(self._home / "auth.json", json.dumps(auth))
                self._log.info("refreshed the Codex login")
            for home in copies:
                home.mkdir(mode=0o700, parents=True, exist_ok=True)
                write_private(home / "auth.json", json.dumps(auth))
        return refreshed

    def _read(self) -> dict[str, Any]:
        try:
            auth = as_dict(json.loads((self._home / "auth.json").read_text(encoding="utf-8")))
        except (OSError, ValueError) as err:
            raise LoginRefreshError(f"no readable Codex login in {self._home}") from err
        if auth is None or not as_dict(auth.get("tokens")):
            raise LoginRefreshError(f"no ChatGPT login in {self._home}")
        return dict(auth)

    def _due(self, auth: Mapping[str, Any]) -> bool:
        stamp = auth.get("last_refresh")
        try:
            last = datetime.fromisoformat(str(stamp)).timestamp()
        except ValueError:
            return True
        return self._clock.now() - last >= self._after

    def _refresh(self, auth: dict[str, Any]) -> dict[str, Any]:
        tokens = dict(as_dict(auth["tokens"]) or {})
        answer = self._post(
            self._url,
            {"client_id": CLIENT_ID, "grant_type": "refresh_token", "refresh_token": str(tokens.get("refresh_token", ""))},
        )
        for key in ("id_token", "access_token", "refresh_token"):
            # A provider may omit a token it did not rotate.
            if answer.get(key):
                tokens[key] = answer[key]
        return {**auth, "tokens": tokens, "last_refresh": datetime.fromtimestamp(self._clock.now(), timezone.utc).isoformat()}


class CodexSlotGuard:
    """A second Codex slot on one host is refused unless the owner has said the shared login is verified."""

    def __init__(self, environ: Mapping[str, str], logger: logging.Logger) -> None:
        self.verified = environ.get(CODEX_SHARED_LOGIN, "") == "verified"
        self._log = logger

    def allows(self, slots: int) -> bool:
        if slots <= 1 or self.verified:
            return True
        self._log.error(
            "refusing %d Codex slots on this host: concurrent refreshes of one ChatGPT login are unproven and can log every slot out."
            " Set %s=verified in run.env once that is settled",
            slots,
            CODEX_SHARED_LOGIN,
        )
        return False
