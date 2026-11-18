"""The workspace credential module: expiry maths, file shape, backoff, the locked refresh and the device login.

Stdlib only and Python 3.9 compatible, because it runs under whatever `python3` a workspace has.
Run as `python3 -I .slop-haus/slop_haus_token.py` (token.sh's shim does), isolated so a planted
module or a PYTHONPATH in an agent's environment cannot shadow the standard library.
"""

from __future__ import annotations

import contextlib
import errno
import fcntl
import glob
import json
import math
import os
import re
import sys
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import dataclass, field
from typing import IO, Any, Callable, Generator, List, Mapping, Optional, Sequence, Tuple, cast

# Matches token.sh: a token this close to expiry is refreshed rather than handed out.
DEFAULT_MARGIN = 60

_DIGITS = re.compile(r"[0-9]+")
_DECIMAL = re.compile(r"[0-9]+(\.[0-9]+)?")


def numeric_or(value: object, default: int) -> int:
    """`value` as a non-negative integer if it is one, else `default`.

    Everything numeric from outside the process (an OAuth response, credentials.json, the
    environment) passes through here, so a hand-edited file or hostile response can't feed
    anything but a plain integer into the arithmetic. Mirrors token.sh's `numeric_or`: only
    ASCII digits, no sign, no whitespace, no floats, and bool is not an int.
    """
    if isinstance(value, bool):
        return default
    if isinstance(value, int):
        return value if value >= 0 else default
    if isinstance(value, str) and _DIGITS.fullmatch(value):
        return int(value)
    return default


def needs_refresh(now: float, expires_at: int, margin: int = DEFAULT_MARGIN) -> bool:
    """True unless the access token stays valid for more than `margin` seconds from `now`."""
    return not (expires_at - now > margin)


@dataclass(frozen=True)
class Credentials:
    # Out of repr, so a stray log line or traceback cannot carry them.
    access_token: str = field(repr=False)
    refresh_token: str = field(repr=False)
    expires_at: int

    def usable(self, now: float, margin: int = DEFAULT_MARGIN) -> bool:
        return bool(self.access_token) and not needs_refresh(now, self.expires_at, margin)

    def to_json(self) -> str:
        """Exactly the three keys, so an older token.sh reads what this wrote."""
        return json.dumps(
            {
                "access_token": self.access_token,
                "refresh_token": self.refresh_token,
                "expires_at": self.expires_at,
            }
        )


def _text(value: Any) -> str:
    return value if isinstance(value, str) else ""


def parse_credentials(text: str) -> Credentials:
    """Read credentials.json tolerantly: unparseable or oddly shaped input yields empty
    credentials (expired, no tokens) rather than an error, and extra keys are ignored."""
    try:
        loaded: object = json.loads(text)
    except ValueError:
        loaded = None
    if not isinstance(loaded, dict):
        return Credentials("", "", 0)
    doc = cast("dict[str, Any]", loaded)
    return Credentials(
        access_token=_text(doc.get("access_token")),
        refresh_token=_text(doc.get("refresh_token")),
        expires_at=numeric_or(doc.get("expires_at"), 0),
    )


def _decimal_or(value: str, default: float) -> float:
    if not _DECIMAL.fullmatch(value):
        return default
    parsed = float(value)
    return parsed if math.isfinite(parsed) else default


@dataclass(frozen=True)
class RefreshPolicy:
    """Retry schedule for a refresh that failed in transit.

    The budget sits inside the 10 seconds Claude Code allows a headersHelper; the rest are
    overridable so tests need not wait in real time.
    """

    budget: int = 7
    attempt_max: int = 4
    attempts: int = 4
    backoff: float = 0.5

    @classmethod
    def from_env(cls, environ: Mapping[str, str]) -> RefreshPolicy:
        raw_backoff = environ.get("SLOP_HAUS_REFRESH_BACKOFF", "")
        return cls(
            budget=numeric_or(environ.get("SLOP_HAUS_REFRESH_BUDGET"), cls.budget),
            attempt_max=numeric_or(environ.get("SLOP_HAUS_REFRESH_ATTEMPT_MAX"), cls.attempt_max),
            attempts=numeric_or(environ.get("SLOP_HAUS_REFRESH_ATTEMPTS"), cls.attempts),
            backoff=_decimal_or(raw_backoff, cls.backoff),
        )

    def delay(self, attempt: int) -> float:
        """Seconds to wait after failed attempt number `attempt` (1-based): doubling from `backoff`."""
        return self.backoff * 2 ** min(max(attempt, 1) - 1, 30)

    def attempt_timeout(self, left: float) -> float:
        """Request timeout for one attempt: what is left of the budget, capped, never under 1s
        (a zero timeout would mean no timeout to curl-like callers)."""
        return max(min(left, self.attempt_max), 1)

    def next_delay(self, attempt: int, left: float) -> Optional[float]:
        """Delay before another attempt after `attempt` failures with `left` seconds of budget,
        or None to give up (attempts used up, or the wait would not leave a second to retry in)."""
        wait = self.delay(attempt)
        if attempt >= self.attempts or left - wait <= 1:
            return None
        return wait


CLIENT_ID = "slop-haus-workspace"
# How long to wait for another of this workspace's processes to finish with the credentials file
# before giving up: long enough to cover a slow refresh, short enough that a wedged process does
# not hang every hook behind it.
LOCK_TIMEOUT = 30.0
_LOCK_POLL = 0.05
_FALLBACK_TTL = 900
_START_TIMEOUT = 15.0
# RFC 6750's b64token alphabet: what `headers` splices into JSON by hand.
_B64TOKEN = re.compile(r"[A-Za-z0-9._~+/-]+=*")


class CredentialsError(Exception):
    """A failure whose message is safe to show: it never carries a token."""


def _refuse_symlink(path: str) -> CredentialsError:
    return CredentialsError(f"slop-haus: {path} is a symlink -- refusing to read or write through it")


def _open_nofollow(path: str, flags: int, mode: int = 0o600) -> int:
    # O_NOFOLLOW closes the check-then-open gap a separate symlink test would leave.
    try:
        return os.open(path, flags | os.O_NOFOLLOW | os.O_CLOEXEC, mode)
    except OSError as err:
        if err.errno == errno.ELOOP:
            raise _refuse_symlink(path) from None
        raise


class CredentialStore:
    """credentials.json and its lock in `<root>/.slop-haus/`."""

    def __init__(self, root: str, lock_timeout: float = LOCK_TIMEOUT) -> None:
        self.directory = os.path.join(root, ".slop-haus")
        self.path = os.path.join(self.directory, "credentials.json")
        self.lock_path = os.path.join(self.directory, "credentials.lock")
        self.lock_timeout = lock_timeout

    def ensure_directory(self) -> None:
        if os.path.islink(self.directory):
            raise _refuse_symlink(self.directory)
        os.makedirs(self.directory, mode=0o700, exist_ok=True)

    def read(self) -> Credentials:
        """What is stored, or empty credentials if there is nothing readable. A symlink is refused."""
        try:
            fd = _open_nofollow(self.path, os.O_RDONLY)
        except FileNotFoundError:
            return Credentials("", "", 0)
        except CredentialsError:
            raise
        except OSError:
            return Credentials("", "", 0)
        with os.fdopen(fd, "r", encoding="utf-8", errors="replace") as handle:
            if os.fstat(handle.fileno()).st_uid != os.geteuid():
                raise CredentialsError(f"slop-haus: {self.path} is not owned by this user -- refusing to read it")
            return parse_credentials(handle.read())

    @contextlib.contextmanager
    def locked(self) -> Generator[None, None, None]:
        """Exclusive flock on credentials.lock, bounded by `lock_timeout`.

        The same advisory flock(2) as util-linux flock(1), so the bash implementation and this one
        exclude each other. O_CLOEXEC keeps the lock out of any child's descriptors.
        """
        fd = _open_nofollow(self.lock_path, os.O_RDWR | os.O_CREAT)
        try:
            if os.fstat(fd).st_uid != os.geteuid():
                raise CredentialsError(f"slop-haus: {self.lock_path} is not owned by this user -- refusing to use it")
            os.fchmod(fd, 0o600)
            deadline = time.monotonic() + self.lock_timeout
            while True:
                try:
                    fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
                    break
                except OSError as err:
                    if err.errno not in (errno.EAGAIN, errno.EACCES):
                        raise
                if time.monotonic() >= deadline:
                    raise CredentialsError(
                        "slop-haus: timed out waiting for another slop-haus process to finish with this workspace's credentials"
                    )
                time.sleep(_LOCK_POLL)
            yield
        finally:
            os.close(fd)

    def write_server_url(self, server: str) -> None:
        """Record where this directory signs in, for token.sh to find when SLOP_HAUS_URL is unset."""
        self._replace_private(os.path.join(self.directory, "server-url"), server.rstrip("/") + "\n", ".server-url.")

    def remove_leftovers(self) -> None:
        """Delete temp files a crash between create and rename left behind: they can hold a spent
        refresh token. Only called under the lock, so no live writer owns one."""
        for pattern in ("credentials.json.*", ".credentials.json.*"):
            for leftover in glob.glob(os.path.join(self.directory, pattern)):
                with contextlib.suppress(OSError):
                    os.unlink(leftover)

    def write(self, creds: Credentials) -> None:
        """Atomically replace credentials.json. The file and the directory are fsynced: the refresh
        token just spent is already gone server-side, so a crash must not resurrect the old name."""
        self._replace_private(self.path, creds.to_json(), "credentials.json.")

    def _replace_private(self, path: str, text: str, prefix: str) -> None:
        fd, tmp = tempfile.mkstemp(prefix=prefix, dir=self.directory)
        try:
            os.fchmod(fd, 0o600)
            with os.fdopen(fd, "w", encoding="utf-8") as handle:
                handle.write(text)
                handle.flush()
                os.fsync(handle.fileno())
            os.replace(tmp, path)
        except BaseException:
            with contextlib.suppress(OSError):
                os.unlink(tmp)
            raise
        dir_fd = os.open(self.directory, os.O_RDONLY | os.O_DIRECTORY | os.O_CLOEXEC)
        try:
            os.fsync(dir_fd)
        finally:
            os.close(dir_fd)


@dataclass(frozen=True)
class Response:
    """An HTTP answer; status 0 means nothing came back at all."""

    status: int
    body: str = field(default="", repr=False)


# fields, request timeout in seconds -> Response. The refresh token travels only inside `fields`,
# in the request body: never argv, never the environment.
Transport = Callable[[str, Sequence[Tuple[str, str]], float], Response]


class _NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args: Any, **kwargs: Any) -> None:
        return None


def post_form(url: str, fields: Sequence[Tuple[str, str]], timeout: float) -> Response:
    request = urllib.request.Request(
        url, data=urllib.parse.urlencode(list(fields)).encode(), method="POST"
    )
    opener = urllib.request.build_opener(_NoRedirect)
    try:
        with opener.open(request, timeout=timeout) as reply:
            return Response(reply.status, reply.read().decode("utf-8", errors="replace"))
    except urllib.error.HTTPError as err:
        with contextlib.closing(err):
            return Response(err.code, err.read().decode("utf-8", errors="replace"))
    except (OSError, ValueError):
        # urllib.error.URLError, timeouts and connection failures are all OSErrors. The text of
        # the exception is dropped: it can echo the request.
        return Response(0)


def _json_object(body: str) -> "dict[str, Any]":
    try:
        loaded: object = json.loads(body)
    except ValueError:
        return {}
    return cast("dict[str, Any]", loaded) if isinstance(loaded, dict) else {}


_MAX_DESCRIPTION = 200


def error_description(body: str, secrets: Sequence[str] = ()) -> str:
    """The server's own words for a refusal, made safe to print: control characters dropped, the
    length bounded, and any secret we sent redacted in case a proxy's page echoed the request."""
    doc = _json_object(body)
    desc = _text(doc.get("error_description")) or _text(doc.get("error")) or "an unrecognised response"
    desc = "".join(ch for ch in desc if ch.isprintable())[:_MAX_DESCRIPTION]
    for secret in secrets:
        if secret:
            desc = desc.replace(secret, "[redacted]")
    return desc


def server_url(root: str, environ: Mapping[str, str]) -> str:
    url = environ.get("SLOP_HAUS_URL", "")
    if not url:
        try:
            with open(os.path.join(root, ".slop-haus", "server-url"), encoding="utf-8") as handle:
                url = handle.readline().strip()
        except OSError:
            url = ""
    if not url:
        raise CredentialsError("slop-haus: no server address -- set SLOP_HAUS_URL, or re-run setup")
    return url.rstrip("/")


class Refresher:
    """Spends a refresh token for a new pair and stores it. Call with the lock held.

    A transit failure (nothing came back, or a 502/503/504 while the server rolls) is retried
    within the policy's budget. That is safe because the server answers the previous refresh token,
    presented again within a few seconds of its rotation, with the pair it already issued; anything
    else, invalid_grant above all, is the server's answer and is never retried.

    Crash window: the sequence is lock, re-read, POST R1, write R2. A crash between the POST and
    the write leaves the file holding the spent R1. Inside the server's 10 s reuse grace the next
    call gets the same pair back; after it the grant is revoked and a login is needed. That is
    inherent in rotation, and is why the write is atomic and fsynced.
    """

    def __init__(
        self,
        store: CredentialStore,
        policy: RefreshPolicy,
        server: str,
        transport: Transport,
        sleep: Callable[[float], None] = time.sleep,
        clock: Callable[[], float] = time.time,
        monotonic: Callable[[], float] = time.monotonic,
    ) -> None:
        self.store = store
        self.policy = policy
        self.server = server
        self.transport = transport
        self.sleep = sleep
        self.clock = clock
        self.monotonic = monotonic

    def refresh(self, refresh_token: str) -> str:
        """The new access token."""
        deadline = self.monotonic() + self.policy.budget
        attempt = 0
        while True:
            left = deadline - self.monotonic()
            reply = self.transport(
                f"{self.server}/oauth/token",
                [
                    ("grant_type", "refresh_token"),
                    ("refresh_token", refresh_token),
                    ("client_id", CLIENT_ID),
                ],
                self.policy.attempt_timeout(left),
            )
            if reply.status == 200:
                return self._store_pair(reply.body)
            if reply.status not in (0, 502, 503, 504):
                self._reject(reply, refresh_token)
            attempt += 1
            wait = self.policy.next_delay(attempt, deadline - self.monotonic())
            if wait is None:
                raise CredentialsError(
                    f"slop-haus: could not reach {self.server} to refresh this workspace's credentials"
                    f" (gave up after {attempt} attempt(s))"
                )
            self.sleep(wait)
            # Another process may have refreshed meanwhile (a login does not wait for the lock),
            # in which case its tokens are the ones to use.
            current = self.store.read()
            if current.usable(self.clock()):
                return current.access_token
            if current.refresh_token:
                refresh_token = current.refresh_token

    def _reject(self, reply: Response, refresh_token: str) -> None:
        if _text(_json_object(reply.body).get("error")) == "invalid_grant":
            # Not deleted: the file is evidence of when this happened, and the next login
            # overwrites it atomically.
            raise CredentialsError(
                "slop-haus: this workspace's credentials were rejected -- run '.slop-haus/token.sh login' to re-authorize it"
            )
        raise CredentialsError(
            f"slop-haus: {self.server} refused to refresh this workspace's credentials: {error_description(reply.body, [refresh_token])}"
        )

    def _store_pair(self, body: str) -> str:
        doc = _json_object(body)
        access = _text(doc.get("access_token"))
        refresh = _text(doc.get("refresh_token"))
        if not access or not refresh:
            raise CredentialsError(f"slop-haus: {self.server} sent an unrecognisable refresh response")
        ttl = numeric_or(doc.get("expires_in"), _FALLBACK_TTL)
        try:
            self.store.write(Credentials(access, refresh, int(self.clock()) + ttl))
        except OSError:
            raise CredentialsError("slop-haus: could not save this workspace's refreshed credentials") from None
        return access


_DEVICE_GRANT = "urn:ietf:params:oauth:grant-type:device_code"
_SLOW_DOWN_STEP = 5


def _positive_or(value: object, default: int) -> int:
    parsed = numeric_or(value, default)
    return parsed if parsed > 0 else default


class DeviceLogin:
    """RFC 8628's device flow: ask for a code, show a person where to approve it, poll until they do.

    Only the network half. Storing what comes back is the caller's, under the credentials lock, so
    the lock is not held across the (human-paced) poll. The device code is as good as a password
    until the poll ends: it travels only in request bodies, and no message here carries it.
    """

    def __init__(
        self,
        server: str,
        transport: Transport,
        announce: Callable[[str], None],
        sleep: Callable[[float], None] = time.sleep,
        monotonic: Callable[[], float] = time.monotonic,
        clock: Callable[[], float] = time.time,
        subject: str = "workspace",
        rerun: str = ".slop-haus/token.sh login",
    ) -> None:
        # Normalised here: a configured "https://host/" must not become "//oauth/...".
        self.server = server.rstrip("/")
        self.transport = transport
        self.announce = announce
        self.sleep = sleep
        self.monotonic = monotonic
        self.clock = clock
        self.subject = subject
        self.rerun = rerun

    def sign_in(self, hints: Mapping[str, str]) -> Credentials:
        """`hints` ride on the verification URL to preselect choices on the consent screen."""
        # No resource parameter: it is optional, and self.server is merely wherever this workspace
        # was told to send requests, which is not always the server's canonical public address.
        started = self.transport(f"{self.server}/oauth/device_authorization", [("client_id", CLIENT_ID)], _START_TIMEOUT)
        if started.status == 0:
            raise CredentialsError(f"slop-haus: could not reach {self.server}")
        if started.status != 200:
            raise CredentialsError(f"slop-haus: {self.server} would not start sign-in: {error_description(started.body)}")
        grant = _json_object(started.body)
        device_code = _text(grant.get("device_code"))
        user_code = _text(grant.get("user_code"))
        url = _text(grant.get("verification_uri_complete"))
        if not (device_code and user_code and url):
            raise CredentialsError(f"slop-haus: {self.server} sent an unrecognisable device authorization response")
        expires_in = numeric_or(grant.get("expires_in"), 600)
        interval = _positive_or(grant.get("interval"), 5)
        url += "".join(f"&{key}={urllib.parse.quote(value, safe='')}" for key, value in hints.items())
        self.announce(
            f"\nOpen this URL to approve this {self.subject}'s access to slop-haus:\n\n  {url}\n\n"
            f"Or open the device page and enter this code by hand: {user_code}\n\n"
            f"This code expires in {expires_in // 60} minute(s).\nWaiting for approval..."
        )
        deadline = self.monotonic() + expires_in
        while self.monotonic() < deadline:
            self.sleep(interval)
            answer = self.transport(
                f"{self.server}/oauth/token",
                [("grant_type", _DEVICE_GRANT), ("device_code", device_code), ("client_id", CLIENT_ID)],
                _START_TIMEOUT,
            )
            if answer.status == 0:
                raise CredentialsError(f"slop-haus: could not reach {self.server}")
            if answer.status == 200:
                return self._tokens(answer.body)
            error = _text(_json_object(answer.body).get("error"))
            if error == "authorization_pending":
                continue
            if error == "slow_down":
                # RFC 8628 section 3.5: poll slower for the rest of this run.
                interval += _SLOW_DOWN_STEP
                continue
            if error == "access_denied":
                raise CredentialsError("slop-haus: sign-in was denied")
            if error == "expired_token":
                raise CredentialsError(f"slop-haus: the code expired before it was approved -- run '{self.rerun}' again")
            raise CredentialsError(f"slop-haus: {self.server}: {error_description(answer.body, [device_code])}")
        raise CredentialsError(f"slop-haus: timed out waiting for approval -- run '{self.rerun}' again")

    def _tokens(self, body: str) -> Credentials:
        doc = _json_object(body)
        access = _text(doc.get("access_token"))
        refresh = _text(doc.get("refresh_token"))
        if not access or not refresh:
            raise CredentialsError(f"slop-haus: {self.server} sent an unrecognisable token response")
        return Credentials(access, refresh, int(self.clock()) + numeric_or(doc.get("expires_in"), _FALLBACK_TTL))


def recorded_agent_name(root: str) -> str:
    """SLOP_HAUS_AGENT from .slop-haus.env, or "".

    Parsed, never sourced: the file sits at the repo root, outside the gitignored .slop-haus/, so a
    pulled commit could carry one. Only the single-quoted form setup writes is accepted.
    """
    path = os.path.join(root, ".slop-haus.env")
    if os.path.islink(path):
        return ""
    try:
        with open(path, encoding="utf-8", errors="replace") as handle:
            lines = handle.read().splitlines()
    except OSError:
        return ""
    for line in lines:
        if line.startswith("SLOP_HAUS_AGENT="):
            value = line[len("SLOP_HAUS_AGENT=") :]
            if len(value) >= 2 and value.startswith("'") and value.endswith("'"):
                return value[1:-1].replace("'\\''", "'")
            return ""
    return ""


def get_token(store: CredentialStore, make_refresher: Callable[[], Refresher], clock: Callable[[], float] = time.time) -> str:
    """A currently valid access token, refreshing under the lock if needed."""
    creds = store.read()
    if creds.usable(clock()):
        return creds.access_token
    with store.locked():
        store.remove_leftovers()
        # Re-read: whoever held the lock before us may have just refreshed, and then there is
        # nothing to ask the server, which is the point of locking before checking.
        creds = store.read()
        if creds.usable(clock()):
            return creds.access_token
        if not creds.refresh_token:
            raise CredentialsError("slop-haus: this workspace has no stored credentials -- run '.slop-haus/token.sh login'")
        return make_refresher().refresh(creds.refresh_token)


def headers_json(token: str) -> str:
    # Spliced into JSON by hand, so refuse anything outside the b64token alphabet rather than
    # trust a tampered credentials.json not to carry a quote or a backslash.
    if not _B64TOKEN.fullmatch(token):
        raise CredentialsError("slop-haus: this workspace's stored access token is malformed -- run '.slop-haus/token.sh login'")
    return '{"Authorization":"Bearer %s"}\n' % token


def main(
    argv: Sequence[str],
    root: str,
    environ: Mapping[str, str],
    transport: Transport = post_form,
    sleep: Callable[[float], None] = time.sleep,
    stdout: Optional[IO[str]] = None,
    stderr: Optional[IO[str]] = None,
) -> int:
    """token.sh's CLI: no arguments prints a token, `headers [-server=URL]` prints MCP headersHelper
    JSON, `login [agent-name]` walks the device flow. Returns the exit status."""
    out = stdout or sys.stdout
    err = stderr or sys.stderr
    # Nothing written here should be readable by anyone else.
    os.umask(0o077)
    args: List[str] = list(argv)
    env = dict(environ)
    command = args[0] if args else ""
    # Codex's http_headers_helper wants the same JSON object Claude's headersHelper does.
    if command == "--headers-json":
        command = "headers"
    if command not in ("", "headers", "login"):
        err.write("usage: token.sh [login [agent-name] | headers|--headers-json [-server=URL]]\n")
        return 2
    if command == "headers":
        for arg in args[1:]:
            if arg.startswith(("-server=", "--server=")):
                env["SLOP_HAUS_URL"] = arg.split("=", 1)[1]
            else:
                err.write("usage: token.sh headers [-server=URL]\n")
                return 2
    try:
        store = CredentialStore(root)
        store.ensure_directory()
        if command == "login":
            # A bare re-login falls back to the name setup recorded, so the approver need not
            # pick this workspace's agent out of the fleet.
            name = (args[1] if len(args) > 1 else "") or env.get("SLOP_HAUS_AGENT", "") or recorded_agent_name(root)

            def say(text: str) -> None:
                # Flushed because the code is read from a pipe by whoever runs this (a setup script,
                # an e2e): block-buffered, it would sit unseen until the poll loop ends.
                print(text, file=out)
                out.flush()

            flow = DeviceLogin(server_url(root, env), transport, say, sleep)
            creds = flow.sign_in({"agent": name} if name else {})
            with store.locked():
                store.remove_leftovers()
                try:
                    store.write(creds)
                except OSError:
                    raise CredentialsError("slop-haus: could not save this workspace's credentials") from None
            out.write("Authorized. Credentials stored in .slop-haus/credentials.json.\n")
            out.flush()
            return 0

        def make_refresher() -> Refresher:
            return Refresher(store, RefreshPolicy.from_env(env), server_url(root, env), transport, sleep)

        token = get_token(store, make_refresher)
        out.write(headers_json(token) if command == "headers" else token)
        out.flush()
        return 0
    except CredentialsError as failure:
        err.write(f"{failure}\n")
        return 1
    except Exception as failure:  # noqa: BLE001 - last resort; the type alone, never the text
        err.write(f"slop-haus: unexpected {type(failure).__name__} while reading this workspace's credentials\n")
        return 1


if __name__ == "__main__":
    _here = os.path.dirname(os.path.abspath(__file__))
    sys.exit(main(sys.argv[1:], os.path.dirname(_here), os.environ))
