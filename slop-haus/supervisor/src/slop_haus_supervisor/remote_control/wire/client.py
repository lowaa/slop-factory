from __future__ import annotations

import json
import logging
import threading
import urllib.error
import urllib.request
from typing import Any, Protocol

from slop_haus_supervisor.net import opener_without_redirects
from slop_haus_supervisor.remote_control.wire.errors import ServerError, Unreachable
from slop_haus_supervisor.slophaus.token import TokenUnavailable
from slop_haus_supervisor.text import clean_text

log = logging.getLogger(__name__)

# Giving the Remote Control up on the way out must never hold the exit: the
# server's host timeout covers a release that does not get through.
RELEASE_TIMEOUT_SECONDS = 5.0

JsonObject = dict[str, Any]


class Tokens(Protocol):
    def get(self, rejected: str = "") -> str:
        """A token to send; `rejected` is one the server just refused, to be replaced."""
        ...


class Client:
    """JSON over HTTP to slop-haus, authenticated as the conductor."""

    def __init__(self, server: str, tokens: Tokens) -> None:
        self.server = server.rstrip("/")
        self.tokens = tokens
        self.opener = opener_without_redirects()

    def post(self, path: str, body: JsonObject, timeout: float) -> JsonObject:
        rejected = ""
        for attempt in (0, 1):
            try:
                token = self.tokens.get(rejected)
            except TokenUnavailable as e:
                raise Unreachable(clean_text(str(e), 300)) from e
            data = json.dumps(body).encode()
            request = urllib.request.Request(
                self.server + path,
                data=data,
                method="POST",
                headers={
                    "Content-Type": "application/json",
                    "Authorization": "Bearer " + token,
                },
            )
            try:
                with self.opener.open(request, timeout=timeout) as response:
                    return json.loads(response.read() or b"{}")
            except urllib.error.HTTPError as e:
                if e.code == 401 and attempt == 0:
                    rejected = token
                    continue
                raise ServerError(e.code, self.detail(e)) from e
            except (urllib.error.URLError, OSError, ValueError) as e:
                raise Unreachable(str(e)) from e
        raise Unreachable("unreachable")

    def post_within(self, path: str, body: JsonObject, timeout: float) -> JsonObject:
        """post, with the token fetch and the 401 retry inside the bound too:
        `timeout` only covers one HTTP exchange. The call is left to finish on
        a daemon thread when the bound passes."""
        outcome: list[JsonObject | BaseException] = []
        done = threading.Event()

        def run() -> None:
            try:
                outcome.append(self.post(path, body, timeout))
            except BaseException as e:
                outcome.append(e)
            done.set()

        threading.Thread(target=run, name="client-post", daemon=True).start()
        if not done.wait(timeout):
            raise Unreachable(f"no answer within {timeout:g}s")
        result = outcome[0]
        if isinstance(result, BaseException):
            raise result
        return result

    def release(self, host_id: str, timeout: float = RELEASE_TIMEOUT_SECONDS) -> bool:
        """Tell the server this host process is done, so the next one is not
        refused until the server's host timeout passes. Whether it was told;
        a failure is logged and nothing more."""
        try:
            self.post_within("/api/conductor/release", {"host_id": host_id}, timeout)
        except (Unreachable, ServerError) as e:
            log.warning(
                "could not release the Remote Control, a new host process waits for the server's timeout: %s",
                clean_text(str(e), 300),
            )
            return False
        return True

    @staticmethod
    def detail(e: urllib.error.HTTPError) -> str:
        try:
            doc = json.loads(e.read())
            return clean_text(str(doc.get("detail") or doc.get("title") or ""), 300)
        except (ValueError, OSError, AttributeError):
            return ""
