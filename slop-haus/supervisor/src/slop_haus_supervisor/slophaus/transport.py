from __future__ import annotations

import json
from email.message import Message
import urllib.error
import urllib.parse
import urllib.request
from collections.abc import Mapping
from dataclasses import dataclass, field
from typing import Any, cast

from slop_haus_supervisor.net import opener_without_redirects
from slop_haus_supervisor.workspace.config import ConfigError


class ApiError(ConfigError):
    """A call that failed; a ConfigError so the command line reports it as a message, not a trace."""

    def __init__(self, message: str, status: int = 0) -> None:
        super().__init__(message)
        self.status = status


def _lowered(headers: Message) -> dict[str, str]:
    return {name.lower(): value for name, value in headers.items()}


@dataclass(frozen=True)
class Response:
    status: int
    body: bytes
    # Lower-cased names.
    headers: Mapping[str, str] = field(default_factory=lambda: {})

    def json(self) -> dict[str, Any]:
        try:
            parsed = json.loads(self.body)
        except ValueError:
            return {}
        return cast(dict[str, Any], parsed) if isinstance(parsed, dict) else {}

    @property
    def ok(self) -> bool:
        return 200 <= self.status < 300

    def detail(self) -> str:
        """The server's own words for a refusal; never the body of a success, which may hold tokens."""
        parsed = self.json()
        for key in ("detail", "error_description", "error"):
            value = parsed.get(key)
            if isinstance(value, str) and value:
                return value
        return f"HTTP {self.status}"


class Transport:
    """One server's HTTP, with the answers of a refusal returned and a connection failure raised."""

    def __init__(self, server: str, timeout: float = 30.0) -> None:
        self._server = server.rstrip("/")
        parsed = urllib.parse.urlparse(self._server)
        # Bearer tokens and the sign-in itself ride on this; plain http is for a machine's own loopback only.
        if parsed.scheme != "https" and parsed.hostname not in ("localhost", "127.0.0.1", "::1"):
            raise ApiError(f"{self._server} is not https: refusing to send credentials over it")
        self._timeout = timeout
        self._opener = opener_without_redirects()

    @property
    def server(self) -> str:
        return self._server

    def send(
        self,
        method: str,
        path: str,
        *,
        token: str = "",
        json_body: object | None = None,
        form: dict[str, str] | None = None,
    ) -> Response:
        headers: dict[str, str] = {}
        data: bytes | None = None
        if token:
            headers["Authorization"] = f"Bearer {token}"
        if json_body is not None:
            data = json.dumps(json_body).encode()
            headers["Content-Type"] = "application/json"
        elif form is not None:
            data = urllib.parse.urlencode(form).encode()
            headers["Content-Type"] = "application/x-www-form-urlencoded"
        request = urllib.request.Request(self._server + path, data=data, method=method, headers=headers)
        try:
            with self._opener.open(request, timeout=self._timeout) as response:
                return Response(response.status, response.read(), _lowered(response.headers))
        except urllib.error.HTTPError as err:
            with err:
                return Response(err.code, err.read(), _lowered(err.headers))
        except OSError as err:
            raise ApiError(f"could not reach {self._server}: {err}") from err
