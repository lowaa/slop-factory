from __future__ import annotations

import plistlib
import re
from abc import ABC, abstractmethod
from dataclasses import dataclass
from pathlib import Path

from slop_haus_supervisor.launcher.activity import read_tail
from slop_haus_supervisor.ports import ProcessRunner
from slop_haus_supervisor.service.run_command import run_argv
from slop_haus_supervisor.workspace.layout import Layout

# The default drain (120s) plus the scheduler's join grace and the kill grace.
STOP_SECONDS = 150
# systemctl stop and launchctl bootout wait for that drain.
STOP_TIMEOUT = STOP_SECONDS + 30.0
RESTART_SECONDS = 10


@dataclass(frozen=True)
class UnitState:
    """What the manager says about the supervisor's process: running, or how it last exited (None: not yet)."""

    running: bool
    exit_status: int | None = None
    # Mid-stop: draining, with the stop already signalled.
    stopping: bool = False


class ServiceManager(ABC):
    """One launcher's unit for one tenant: what to write, and the commands that load, start, stop and read it."""

    def __init__(self, layout: Layout, tenant: str, runner: ProcessRunner, home: Path) -> None:
        self._layout = layout
        self._tenant = tenant
        self._runner = runner
        self._home = home

    @property
    @abstractmethod
    def name(self) -> str:
        """The manager, for messages."""

    @property
    @abstractmethod
    def unit_path(self) -> Path: ...

    @abstractmethod
    def render(self, uv: str) -> str: ...

    @abstractmethod
    def load(self) -> None:
        """Makes the manager read the unit and start it at login, failing with `ServiceError` if it refuses."""

    @abstractmethod
    def start(self) -> None: ...

    @abstractmethod
    def stop(self) -> None:
        """Stops it, draining; returns when it has stopped."""

    @abstractmethod
    def unload(self) -> None:
        """The reverse of `load`."""

    @abstractmethod
    def state(self) -> UnitState: ...

    @abstractmethod
    def recent_log(self, lines: int) -> list[str]: ...

    def linger_note(self) -> str | None:
        return None

    def _run(self, argv: list[str], timeout: float = 30.0, check: bool = True) -> str:
        done = self._runner.run(argv, self._home, timeout)
        if check and done.returncode != 0:
            raise ServiceError(f"`{' '.join(argv)}` failed: {(done.stderr or done.stdout).strip()}")
        return done.stdout


class ServiceError(Exception):
    pass


def _systemd_word(word: str) -> str:
    # systemd expands %-specifiers and splits on whitespace, quoting its own way.
    word = word.replace("%", "%%").replace("$", "$$")
    if re.search(r'[\s"\'\\]', word):
        return '"' + word.replace("\\", "\\\\").replace('"', '\\"') + '"'
    return word


class SystemdUser(ServiceManager):
    def __init__(self, layout: Layout, tenant: str, runner: ProcessRunner, home: Path, user: str) -> None:
        super().__init__(layout, tenant, runner, home)
        self._user = user

    @property
    def name(self) -> str:
        return "systemd"

    @property
    def unit_name(self) -> str:
        return f"slop-haus-supervisor@{self._tenant}.service"

    @property
    def unit_path(self) -> Path:
        return self._home / ".config" / "systemd" / "user" / self.unit_name

    def render(self, uv: str) -> str:
        command = " ".join(_systemd_word(w) for w in run_argv(uv, self._layout))
        return f"""\
# Written by `slop-haus service install`; run that again rather than editing this.
[Unit]
Description=slop-haus supervisor for tenant {self._tenant}
After=network-online.target

[Service]
Type=simple
# The supervisor reads <root>/secrets.env and <root>/run.env itself, so no environment is set here.
# uv is found before run.env is read, so its path is absolute.
ExecStart={command}
# 0 (stopped on request), 75 (another run holds the tenant) and 78 (configuration error) are
# not retried; anything else, including 3 (a newer supervisor is wanted), is.
Restart=on-failure
RestartSec={RESTART_SECONDS}
RestartPreventExitStatus=75 78
# SIGTERM goes to the supervisor alone and it drains; whatever is left when the stop timeout ends is killed.
KillMode=mixed
TimeoutStopSec={STOP_SECONDS}

[Install]
WantedBy=default.target
"""

    def _systemctl(self, *args: str, timeout: float = 30.0, check: bool = True) -> str:
        return self._run(["systemctl", "--user", *args], timeout, check)

    def load(self) -> None:
        self._systemctl("daemon-reload")
        self._systemctl("enable", self.unit_name)

    def start(self) -> None:
        self._systemctl("start", self.unit_name, "--no-block")

    def stop(self) -> None:
        self._systemctl("stop", self.unit_name, timeout=STOP_TIMEOUT)

    def unload(self) -> None:
        self._systemctl("disable", self.unit_name, check=False)

    def state(self) -> UnitState:
        out = self._systemctl("show", self.unit_name, "-p", "ActiveState", "-p", "ExecMainStatus", check=False)
        shown = {key: value for key, _, value in (line.partition("=") for line in out.splitlines())}
        status = shown.get("ExecMainStatus", "")
        # `activating (auto-restart)` is a failed start waiting to be retried, not a running supervisor.
        running = shown.get("ActiveState") == "active"
        exit_status = int(status) if status.isdigit() and status != "0" and not running else None
        return UnitState(running, exit_status, shown.get("ActiveState") == "deactivating")

    def recent_log(self, lines: int) -> list[str]:
        out = self._run(["journalctl", "--user", "-u", self.unit_name, "-n", str(lines), "--no-pager", "-o", "cat"], check=False)
        return out.splitlines()

    def linger_note(self) -> str | None:
        shown = self._run(["loginctl", "show-user", self._user, "-p", "Linger"], check=False).strip()
        if shown == "Linger=yes":
            return None
        return (
            f"lingering is off, so this unit stops when {self._user} logs out. Run once, as root:\n"
            f"  sudo loginctl enable-linger {self._user}"
        )


class Launchd(ServiceManager):
    def __init__(self, layout: Layout, tenant: str, runner: ProcessRunner, home: Path, uid: int) -> None:
        super().__init__(layout, tenant, runner, home)
        self._domain = f"gui/{uid}"

    @property
    def name(self) -> str:
        return "launchd"

    @property
    def label(self) -> str:
        return f"com.slop-haus.supervisor.{self._tenant}"

    @property
    def unit_path(self) -> Path:
        return self._home / "Library" / "LaunchAgents" / f"{self.label}.plist"

    def render(self, uv: str) -> str:
        logs = self._home / "Library" / "Logs"
        agent = {
            "Label": self.label,
            "ProgramArguments": run_argv(uv, self._layout),
            "RunAtLoad": True,
            # Restart after a failure, not after exit 0 (stopped on request). launchd cannot tell exit codes
            # apart, so a configuration error (78) or a second instance (75) is retried every ThrottleInterval.
            "KeepAlive": {"SuccessfulExit": False},
            "ThrottleInterval": 30,
            "ExitTimeOut": STOP_SECONDS,
            # The supervisor writes its own rotated log; these two only catch what happens before it starts.
            "StandardOutPath": "/dev/null",
            "StandardErrorPath": str(logs / f"slop-haus-supervisor-{self._tenant}.stderr.log"),
        }
        return plistlib.dumps(agent).decode()

    def _launchctl(self, *args: str, timeout: float = 30.0, check: bool = True) -> str:
        return self._run(["launchctl", *args], timeout, check)

    @property
    def _target(self) -> str:
        return f"{self._domain}/{self.label}"

    def load(self) -> None:
        # A loaded agent keeps its old definition until it is booted out.
        self._launchctl("bootout", self._target, timeout=STOP_TIMEOUT, check=False)
        self._run(["plutil", "-lint", str(self.unit_path)])
        self._launchctl("bootstrap", self._domain, str(self.unit_path))

    def start(self) -> None:
        # RunAtLoad started it at bootstrap.
        pass

    def stop(self) -> None:
        self._launchctl("bootout", self._target, timeout=STOP_TIMEOUT, check=False)

    def unload(self) -> None:
        self.stop()

    def state(self) -> UnitState:
        printed = self._launchctl("print", self._target, check=False)
        status = re.search(r"last exit code = (\d+)", printed)
        running = "state = running" in printed
        return UnitState(running, int(status.group(1)) if status and status.group(1) != "0" and not running else None)

    def recent_log(self, lines: int) -> list[str]:
        return read_tail(self._layout.root / "supervisor.log")[-lines:]
