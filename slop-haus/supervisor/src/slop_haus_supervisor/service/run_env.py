from __future__ import annotations

import os
import re
from collections.abc import Callable, MutableMapping
from pathlib import Path

from slop_haus_supervisor.workspace import envfile


def apply(path: Path, environ: MutableMapping[str, str]) -> list[str]:
    """Applies <root>/run.env to this process's environment, which every worker run inherits.

    The file is parsed, never sourced, so the launcher needs no shell. PATH is prepended to
    the launcher's rather than replacing it (launchd and Task Scheduler start with a minimal one);
    every other name is set outright, except a `SLOP_HAUS_` name this supervisor does not know: that is dropped, as
    `LayeredEnv` drops it (decision 0009), so it reaches neither this process nor its children.
    Returns the names, never the values.
    """
    values = {k: v for k, v in envfile.read(path).items() if not k.startswith("SLOP_HAUS_") or k in OWN_SETTINGS}
    for name, value in values.items():
        if name == "PATH":
            current = environ.get("PATH", "")
            environ["PATH"] = value + os.pathsep + current if current else value
        else:
            environ[name] = value
    return list(values)


def apply_git_defaults(environ: MutableMapping[str, str]) -> None:
    """Git and ssh never prompt: nobody is at the terminal, and a prompt would hang a run for ever."""
    environ.setdefault("GIT_TERMINAL_PROMPT", "0")
    environ.setdefault("GIT_SSH_COMMAND", "ssh -o BatchMode=yes")


class LauncherEnvError(Exception):
    pass


_NAME = re.compile(r"[A-Za-z_][A-Za-z0-9_]*")
# Names run.env itself may set. Never an included file (a repo's) and never the agent's session: the
# variables are read once, by the launcher, before any agent starts.
# The subset the harnesses also pass to worker runs: token.sh reads SLOP_HAUS_TOKEN_IMPL from inside hooks and MCP
# auth headers. SLOP_HAUS_LABELS is the supervisor's own (it describes the session) and runs have no use for it.
RUN_SETTINGS = frozenset({"SLOP_HAUS_TOKEN_IMPL"})
# The owner's word that one Codex login may serve several slots (see CodexSlotGuard); the supervisor reads it, runs do not.
CODEX_SHARED_LOGIN = "SLOP_HAUS_CODEX_SHARED_LOGIN"
# The path to slop-review's review.sh, which the supervisor runs on an agent's pushed head; runs never see it.
REVIEW_SCRIPT = "SLOP_HAUS_REVIEW_SCRIPT"
OWN_SETTINGS = RUN_SETTINGS | {"SLOP_HAUS_LABELS", CODEX_SHARED_LOGIN, REVIEW_SCRIPT}
_SHELL_CONTROLLING = frozenset(
    "BASH_ENV ENV BASHOPTS SHELLOPTS PS4 PROMPT_COMMAND IFS CDPATH GLOBIGNORE PATH NODE_OPTIONS "
    "PYTHONSTARTUP PYTHONPATH PERL5OPT RUBYOPT".split()
)


def controls_execution(name: str) -> bool:
    """Whether a variable changes what a shell or the dynamic loader runs rather than just carrying a value."""
    return name in _SHELL_CONTROLLING or name.startswith(("LD_", "DYLD_", "BASH_FUNC_"))


def _lines(path: Path) -> list[str]:
    text = path.read_bytes().decode("utf-8", errors="surrogateescape")
    lines = text.split("\n")
    # A file ending in a newline has no last line after it; one without still has its last.
    return lines[:-1] if lines and lines[-1] == "" else lines


class LauncherEnv:
    """<workspace>/.slop-haus/run.env as the Remote Control launcher reads it: parsed, never sourced.

    The grammar is deliberately smaller than `envfile`'s (no inline comments, no escapes, and a line that
    is not an assignment is an error rather than skipped), because the bash launcher's rules are what
    owners have written their files against. An optional `export `, matching quotes around the value, no
    expansion, and `< path NAME...` takes the named variables from one more file, one level deep. An
    included file is a repo's, which nobody here vouches for: only the names asked for are taken, by
    allowlist (no list of dangerous names is ever complete), and the rest of it is none of our business.
    `SLOP_HAUS_*` names are slop-haus's own and are ignored, except the operator settings (OWN_SETTINGS) set in run.env itself.
    """

    def __init__(self, root: Path, warn: Callable[[str], None]) -> None:
        self._root = root
        self._warn = warn

    def load(self, run_env: Path) -> dict[str, str]:
        """The variables to set, or nothing when there is no run.env; raises on anything the launcher refuses.

        Nothing is applied until the whole file has been read, so a refused line never leaves half of it set.
        """
        if not os.path.lexists(run_env):
            return {}
        if not self._inside(run_env):
            raise LauncherEnvError(f"{run_env} leads outside this workspace -- refusing to read it")
        values: dict[str, str] = {}
        self._read(run_env, None, values)
        return values

    def _inside(self, path: Path) -> bool:
        try:
            real, real_root = path.resolve(strict=True), self._root.resolve(strict=True)
        except OSError:
            return False
        return real.is_relative_to(real_root) and real != real_root

    def _read(self, file: Path, allowed: set[str] | None, values: dict[str, str]) -> None:
        for n, raw in enumerate(_lines(file), start=1):
            line = raw.removesuffix("\r").lstrip(" \t\n\r\f\v")
            if not line or line.startswith("#"):
                continue
            where = f"{file}:{n}"
            if line.startswith("<"):
                # One level deep: an included file's own includes are ignored like the rest of what it holds.
                if allowed is None:
                    self._include(where, line[1:], values)
                continue
            line = line.removeprefix("export ")
            name, has_value, value = line.partition("=")
            if allowed is not None and (not has_value or name not in allowed):
                continue
            if not has_value or not _NAME.fullmatch(name):
                raise LauncherEnvError(f"{where}: expected NAME=value")
            if name.startswith("SLOP_HAUS_") and not (allowed is None and name in OWN_SETTINGS):
                self._warn(f"{where}: ignoring {name} -- slop-haus settings come from this workspace's own files")
                continue
            if controls_execution(name):
                raise LauncherEnvError(f"{where}: refusing {name} -- it changes what every shell or program the agent starts runs")
            if len(value) >= 2 and value[0] == value[-1] and value[0] in "\"'":
                value = value[1:-1]
            elif value[:1] in ("'", '"') or value[-1:] in ("'", '"'):
                raise LauncherEnvError(f"{where}: unmatched quote in {name}")
            values[name] = value

    def _include(self, where: str, rest: str, values: dict[str, str]) -> None:
        parts = rest.split()
        if len(parts) < 2:
            raise LauncherEnvError(f"{where}: expected '< path NAME...' -- name the variables to take from it")
        rel, names = parts[0], set(parts[1:])
        target = self._root / rel
        if not os.path.exists(target):
            self._warn(f"{where}: {rel} does not exist; skipping it")
            return
        if not self._inside(target):
            raise LauncherEnvError(f"{where}: {rel} is outside this workspace -- refusing to read it")
        self._read(target, names, values)
