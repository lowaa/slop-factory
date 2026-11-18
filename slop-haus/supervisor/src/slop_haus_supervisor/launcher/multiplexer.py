from __future__ import annotations

import re
import shutil
from abc import ABC, abstractmethod
from collections.abc import Callable
from pathlib import Path

from slop_haus_supervisor.ports import ProcessRunner


class Multiplexer(ABC):
    """A terminal multiplexer that can hold the supervisor's foreground process detached."""

    binary: str

    def __init__(self, runner: ProcessRunner) -> None:
        self._runner = runner

    @abstractmethod
    def exists(self, session: str) -> bool: ...

    @abstractmethod
    def start_argv(self, session: str, command: list[str], cwd: Path) -> list[str]: ...

    @abstractmethod
    def attach_argv(self, session: str) -> list[str]: ...

    def start(self, session: str, command: list[str], cwd: Path) -> None:
        done = self._runner.run(self.start_argv(session, command, cwd), cwd)
        if done.returncode != 0:
            raise LauncherError(f"{self.binary} could not start the session: {(done.stderr or done.stdout).strip()}")


class LauncherError(Exception):
    pass


class Screen(Multiplexer):
    binary = "screen"

    def exists(self, session: str) -> bool:
        # `screen -ls` exits non-zero both with no sessions and with some, so only its text says.
        done = self._runner.run(["screen", "-ls"], Path.cwd())
        return re.search(rf"\b\d+\.{re.escape(session)}\s", done.stdout) is not None

    def start_argv(self, session: str, command: list[str], cwd: Path) -> list[str]:
        # screen starts the command in its own working directory, which is the one we run it from.
        return ["screen", "-dmS", session, *command]

    def attach_argv(self, session: str) -> list[str]:
        return ["screen", "-r", session]


class Tmux(Multiplexer):
    binary = "tmux"

    def exists(self, session: str) -> bool:
        return self._runner.run(["tmux", "has-session", "-t", f"={session}"], Path.cwd()).returncode == 0

    def start_argv(self, session: str, command: list[str], cwd: Path) -> list[str]:
        return ["tmux", "new-session", "-d", "-s", session, "-c", str(cwd), *command]

    def attach_argv(self, session: str) -> list[str]:
        return ["tmux", "attach-session", "-t", f"={session}"]


def choose(runner: ProcessRunner, which: Callable[[str], str | None] = shutil.which) -> Multiplexer:
    """screen if installed, else tmux; the person who has neither is told what to install or what to use instead."""
    if which("screen"):
        return Screen(runner)
    if which("tmux"):
        return Tmux(runner)
    raise LauncherError(
        "neither screen nor tmux is installed: install one (e.g. `sudo apt install screen`), "
        "or run `slop-haus run` under a service manager (see slop-haus/supervisor/service/)"
    )


class NoMultiplexer(Multiplexer):
    """Stands in where neither is installed and the command only reads (status), so it reports no session."""

    binary = "screen or tmux"

    def exists(self, session: str) -> bool:
        return False

    def start_argv(self, session: str, command: list[str], cwd: Path) -> list[str]:
        raise LauncherError("neither screen nor tmux is installed")

    def attach_argv(self, session: str) -> list[str]:
        raise LauncherError("neither screen nor tmux is installed")


def choose_or_none(runner: ProcessRunner, which: Callable[[str], str | None] = shutil.which) -> Multiplexer:
    try:
        return choose(runner, which)
    except LauncherError:
        return NoMultiplexer(runner)
