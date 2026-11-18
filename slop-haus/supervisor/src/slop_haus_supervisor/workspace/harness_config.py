"""The harness config slop-haus holds for an agent (model, effort / provider, thinking), fetched and saved beside the workspace's files.

This is a trust boundary: what a config becomes runs inside a process that runs tools. Only the names
below are ever produced, never a `SLOP_HAUS_*` name or anything that changes what a shell or the loader
runs, and only values that pass the setting's own pattern. A setting this does not know, or a value that
fails, is left out: the server validated both already, and a newer server's setting is not a reason to
refuse to start.
"""

from __future__ import annotations

import json
import logging
import os
import tempfile
import urllib.error
import urllib.request
from collections.abc import Callable
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, cast

from slop_haus_supervisor.harness import registry
from slop_haus_supervisor.harness.variant import CLAUDE_EFFORT, CODEX_EFFORT, MODEL, PI_THINKING, PROVIDER
from slop_haus_supervisor.net import opener_without_redirects
from slop_haus_supervisor.slophaus.token import TokenUnavailable

HARNESSES = registry.NAMES
# A start waits this long on slop-haus before it goes on with the saved config.
FETCH_TIMEOUT = 5.0
_MAX_BODY = 1 << 20


class HarnessConfigError(Exception):
    pass


def _model(value: str) -> bool:
    return MODEL.fullmatch(value) is not None


def _provider(value: str) -> bool:
    return PROVIDER.fullmatch(value) is not None


def _one_of(options: tuple[str, ...]) -> Callable[[str], bool]:
    return lambda value: value in options


@dataclass(frozen=True)
class HarnessConfig:
    revision: int
    # The harness the agent's variant runs; empty for an agent in no variant (and for a name not known here).
    harness: str
    settings: dict[str, dict[str, Any]] = field(default_factory=lambda: dict[str, dict[str, Any]]())

    @classmethod
    def parse(cls, raw: bytes) -> HarnessConfig | None:
        """None when `raw` is not a config at all (a proxy's error page can arrive with a 200)."""
        try:
            loaded: Any = json.loads(raw)
        except ValueError:
            return None
        if not isinstance(loaded, dict):
            return None
        doc = cast("dict[str, Any]", loaded)
        if not isinstance(doc.get("settings"), dict):
            return None
        revision = doc.get("revision")
        # bool is an int in Python; a revision is not.
        if isinstance(revision, bool) or not isinstance(revision, int) or revision < 1:
            return None
        harness = doc.get("harness")
        return cls(revision, harness if harness in HARNESSES else "", doc["settings"])

    def _setting(self, harness: str, name: str, valid: Callable[[str], bool]) -> str | None:
        settings = self.settings.get(harness)
        value = settings.get(name) if isinstance(settings, dict) else None
        return value if isinstance(value, str) and valid(value) else None

    def env(self, harness: str) -> dict[str, str]:
        """Environment variables for Claude Code; pi's config is flags, see `flags`."""
        if harness != "claude":
            return {}
        out: dict[str, str] = {}
        if (model := self._setting(harness, "model", _model)) is not None:
            out["ANTHROPIC_MODEL"] = model
        if (sub := self._setting(harness, "subagent_model", _model)) is not None:
            out["CLAUDE_CODE_SUBAGENT_MODEL"] = sub
            # A subagent definition may pin its own model, which beats the variable unless force is on.
            out["CLAUDE_CODE_SUBAGENT_MODEL_FORCE"] = "1"
        if (effort := self._setting(harness, "effort", _one_of(CLAUDE_EFFORT))) is not None:
            out["CLAUDE_CODE_EFFORT_LEVEL"] = effort
        return out

    def variant(self, harness: str) -> str:
        """The run's variant text (`model=x,effort=high`) for the harness, from the same validated settings as `env` and `flags`.

        Empty when the config sets nothing for it, which leaves the harness's own defaults. Codex and pi
        refuse a value outside their own choices: unlike a Claude Code setting, a bad one would otherwise
        silently run the slot on the defaults the owner meant to change.
        """
        if harness == "claude":
            parts = {
                "model": self._setting(harness, "model", _model),
                "effort": self._setting(harness, "effort", _one_of(CLAUDE_EFFORT)),
                "subagent_model": self._setting(harness, "subagent_model", _model),
            }
        elif harness == "codex":
            parts = {
                "model": self._setting(harness, "model", _model),
                "effort": self._choice(harness, "effort", CODEX_EFFORT),
            }
        elif harness == "pi":
            parts = {
                "provider": self._setting(harness, "provider", _provider),
                "model": self._setting(harness, "model", _model),
                "thinking": self._choice(harness, "thinking", PI_THINKING),
            }
        else:
            return ""
        return ",".join(f"{name}={value}" for name, value in parts.items() if value is not None)

    def _choice(self, harness: str, name: str, options: tuple[str, ...]) -> str | None:
        settings = self.settings.get(harness)
        value = settings.get(name) if isinstance(settings, dict) else None
        if value is None:
            return None
        if not isinstance(value, str) or value not in options:
            raise HarnessConfigError(f"the {harness} {name} setting {value!r} is not one of {', '.join(options)}")
        return value

    def flags(self, harness: str) -> dict[str, str]:
        """pi's `provider`, `model` and `thinking`, by setting name."""
        if harness != "pi":
            return {}
        found = {
            "provider": self._setting(harness, "provider", _provider),
            "model": self._setting(harness, "model", _model),
            "thinking": self._setting(harness, "thinking", _one_of(PI_THINKING)),
        }
        return {name: value for name, value in found.items() if value is not None}


class HarnessConfigStore:
    """This agent's harness config, fetched from slop-haus and saved as `.slop-haus/harness-config.json`."""

    def __init__(
        self,
        directory: Path,
        server: str,
        token: Callable[[], str],
        logger: logging.Logger,
        timeout: float = FETCH_TIMEOUT,
    ) -> None:
        self._dir = directory / ".slop-haus"
        self._saved = self._dir / "harness-config.json"
        self._server = server.rstrip("/")
        self._token = token
        self._log = logger
        self._timeout = timeout
        self._opener = opener_without_redirects()

    @property
    def path(self) -> Path:
        return self._saved

    def refresh(self) -> HarnessConfig | None:
        """The server's config, saved; None when it says there is none (204), and the saved one removed.

        Returns what this fetch got rather than leaving the caller to read the file back, which another
        launch in the same workspace may have rewritten meanwhile. Only a failure to run at all raises: a
        server that cannot be reached, or answers with something else, leaves the saved config to be used,
        with a warning.
        """
        self._refuse_links(self._dir)
        try:
            token = self._token()
        except (TokenUnavailable, OSError):
            token = ""
        if not self._server or not token:
            return self._unfetched("no server address or token")
        request = urllib.request.Request(self._server + "/api/workspace/harness_config", headers={"Authorization": "Bearer " + token})
        try:
            with self._opener.open(request, timeout=self._timeout) as response:
                status, body = response.status, response.read(_MAX_BODY + 1)
        except urllib.error.HTTPError as err:
            return self._unfetched(f"{self._server} answered {err.code}")
        except (urllib.error.URLError, OSError, TimeoutError):
            return self._unfetched(f"{self._server} could not be reached")
        if status == 204:
            self._saved.unlink(missing_ok=True)
            return None
        if status != 200:
            return self._unfetched(f"{self._server} answered {status}")
        config = HarnessConfig.parse(body) if len(body) <= _MAX_BODY else None
        if config is None:
            return self._unfetched(f"{self._server} did not answer with a harness config")
        self._save(body)
        return config

    def load(self) -> HarnessConfig | None:
        """The saved config; None when there is none. One that is not a config stops the start."""
        self._refuse_links(self._saved)
        try:
            raw = self._saved.read_bytes()
        except FileNotFoundError:
            return None
        config = HarnessConfig.parse(raw)
        if config is None:
            raise HarnessConfigError(f"{self._saved} is not a harness config -- remove it, and start again to fetch a new one")
        return config

    def _save(self, body: bytes) -> None:
        self._refuse_links(self._saved)
        fd, tmp = tempfile.mkstemp(dir=self._dir, prefix="harness-config.")
        try:
            with os.fdopen(fd, "wb") as out:
                out.write(body)
            os.replace(tmp, self._saved)
        except BaseException:
            Path(tmp).unlink(missing_ok=True)
            raise

    def _unfetched(self, why: str) -> HarnessConfig | None:
        if self._saved.exists():
            self._log.warning("could not fetch this agent's harness config (%s) -- using the one saved last time", why)
        else:
            self._log.warning("could not fetch this agent's harness config (%s) -- starting without one; run.env alone applies", why)
        return self.load()

    @staticmethod
    def _refuse_links(*paths: Path) -> None:
        # Each is read or written with this workspace's credentials in reach, so a link would go wherever it points.
        for path in paths:
            if path.is_symlink():
                raise HarnessConfigError(f"{path} is a symlink -- refusing to run through it")
