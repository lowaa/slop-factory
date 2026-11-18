"""The supervisor's view of the shared credential module (`slop_haus_token`, vendored from token/).

The module owns the file shape, the lock and the device flow; this only maps its errors onto the
command line's `ConfigError`, so what a workspace's token.sh does and what the supervisor does cannot drift.
"""

from __future__ import annotations

import time
from collections.abc import Callable, Generator, Mapping
from contextlib import contextmanager
from pathlib import Path

from slop_haus_supervisor.slophaus import slop_haus_token as module
from slop_haus_supervisor.workspace.config import ConfigError

CREDENTIALS = "credentials.json"
LOCK_TIMEOUT = module.LOCK_TIMEOUT
TokenPair = module.Credentials


def _store(directory: Path) -> module.CredentialStore:
    store = module.CredentialStore(str(directory), LOCK_TIMEOUT)
    try:
        store.ensure_directory()
    except module.CredentialsError as err:
        raise ConfigError(str(err)) from None
    return store


@contextmanager
def credentials_lock(directory: Path) -> Generator[None]:
    """Hold `<directory>/.slop-haus/credentials.lock` exclusively, the lock token.sh takes around a refresh.

    token.sh spends a refresh token under it, and the server revokes the grant if one is presented
    twice, so a writer that replaces credentials.json without it can lose the slot's only sign-in.
    flock is on the open file, not the path: renaming `directory` while holding this keeps the
    lock on that same file wherever it moves to.
    """
    try:
        with _store(directory).locked():
            yield
    except module.CredentialsError as err:
        raise ConfigError(str(err)) from None


def write_credentials(directory: Path, server: str, tokens: TokenPair) -> None:
    """Store a sign-in where `token.sh` looks for it: `<directory>/.slop-haus/credentials.json`.

    token.sh owns refreshing it from here on; this only writes the file, in the shape it reads.
    """
    with credentials_lock(directory):
        write_credentials_locked(directory, server, tokens)


def write_credentials_locked(directory: Path, server: str, tokens: TokenPair) -> None:
    """`write_credentials` for a caller already inside `credentials_lock(directory)`, which flock does not let it re-take."""
    store = _store(directory)
    try:
        store.remove_leftovers()
        store.write(tokens)
        store.write_server_url(server)
    except module.CredentialsError as err:
        raise ConfigError(str(err)) from None


def read_credentials(directory: Path) -> TokenPair | None:
    """What `write_credentials` (or token.sh) left in the directory; None when it is absent or not in that shape."""
    store = _store(directory)
    try:
        creds = store.read()
    except module.CredentialsError as err:
        raise ConfigError(str(err)) from None
    if not (creds.access_token and creds.refresh_token):
        return None
    return creds


def device_sign_in(
    server: str,
    hints: Mapping[str, str],
    announce: Callable[[str], None],
    sleep: Callable[[float], None] = time.sleep,
) -> TokenPair:
    """RFC 8628's device flow against `server`; `hints` preselect choices on the consent screen."""
    flow = module.DeviceLogin(server, module.post_form, announce, sleep, subject="supervisor", rerun="login")
    try:
        return flow.sign_in(hints)
    except module.CredentialsError as err:
        raise ConfigError(str(err)) from None
