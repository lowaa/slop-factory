from __future__ import annotations

from collections.abc import Callable
from dataclasses import dataclass
from pathlib import Path

from slop_haus_supervisor.ports import ProcessRunner

Which = Callable[[str], str | None]


@dataclass(frozen=True)
class ToolLookup:
    name: str
    # Where the service finds it; None when it does not.
    service_path: str | None
    # Where the login shell finds it; only looked up when the service cannot.
    login_path: str | None = None

    @property
    def found(self) -> bool:
        return self.service_path is not None

    @property
    def path_fix(self) -> str:
        """The folder run.env's PATH needs for a tool only the login shell has; "" when there is none to name."""
        if self.found or not self.login_path or not Path(self.login_path).is_absolute():
            return ""
        return str(Path(self.login_path).parent)

    def problem(self, run_env: Path) -> str:
        """Why the service cannot run the tool, with the fix; "" when it can."""
        if self.found:
            return ""
        if self.path_fix:
            return (
                f"{self.name} is not on this service's PATH (found at {self.path_fix} in a login shell): "
                f"add that folder to PATH in {run_env} and restart the supervisor"
            )
        return f"{self.name} is not installed, or not on this service's PATH"


class ToolChecker:
    """Whether a tool is findable where the service looks, and if not, where the login shell has it.

    A tool on the shell's PATH can be missing for a service manager, whose environment is not a login shell's.
    Setup and the reconciler both ask here, so they say the same thing.
    """

    def __init__(self, service_which: Which, login_which: Which) -> None:
        self._service_which = service_which
        self._login_which = login_which

    def lookup(self, name: str) -> ToolLookup:
        service = self._service_which(name)
        if service:
            return ToolLookup(name, service)
        return ToolLookup(name, None, self._login_which(name))


def login_shell_which(runner: ProcessRunner, home: Path, shell: str = "bash") -> Which:
    """Where a login shell finds a tool. Interactive too: ~/.bashrc, where tools usually get on PATH, returns early without it."""

    # Remembered: a login shell runs the user's rc files, and the answer only changes when they do.
    answers: dict[str, str | None] = {}

    def find(name: str) -> str | None:
        if name not in answers:
            answers[name] = probe(name)
        return answers[name]

    def probe(name: str) -> str | None:
        try:
            done = runner.run([shell, "-lic", 'command -v -- "$1"', "_", name], home, 20)
        except (OSError, TimeoutError):
            return None
        lines = done.stdout.strip().splitlines() if done.returncode == 0 else []
        return lines[-1] if lines and lines[-1].startswith("/") else None

    return find


def read_tool_list(path: Path) -> list[str]:
    """The tools a host's slots run besides their harness, one name per line; none when the file is absent."""
    try:
        text = path.read_text()
    except OSError:
        return []
    names = (line.split("#", 1)[0].strip() for line in text.splitlines())
    return [n for n in names if n and "/" not in n and not n.startswith("-")]
