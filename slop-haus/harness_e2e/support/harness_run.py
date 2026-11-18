"""Starts a harness process the way a scenario says (a Remote Control host, pi with the host's flags)
and gives the scenario its output and its end. A worker's run is the supervisor's to start: see
supervised_case.py."""

from __future__ import annotations

import fcntl
import os
import re
import signal
import struct
import subprocess
import termios
import threading
from pathlib import Path
from typing import IO

from harnesses.base import HarnessAdapter

from .waiting import wait_for


# What is left of a terminal UI's drawing once the escapes are gone: rules, padding, blank rows.
CLUTTER = re.compile(r"^[ ─│╭╮╰╯]*$\n?|(?<=\S) {3,}", re.MULTILINE)
ANSI = re.compile(rb"\x1b(\[[0-9;?]*[ -/]*[@-~]|\][^\x07\x1b]*(\x07|\x1b\\)|[()][A-Z0-9])|\r")


class HarnessRun:
    def __init__(
        self,
        adapter: HarnessAdapter,
        workspace: Path,
        home: Path,
        model_server_url: str,
        log_path: Path,
        command: list[str],
        extra_env: dict[str, str] | None = None,
        base_env: dict[str, str] | None = None,
        # False when the scenario decides what the owner's own home holds.
        prepare_home: bool = True,
        # None: the adapter decides. True runs a harness that works headless in its terminal UI.
        tty: bool | None = None,
    ) -> None:
        home.mkdir(parents=True, exist_ok=True)
        if prepare_home:
            adapter.prepare(home, workspace, model_server_url)
        # A harness runs model-chosen commands, so it gets a minimal
        # environment, not the test's own (which holds the board password):
        # what a shell needs, and what the adapter says it needs.
        env = {
            **{k: v for k, v in os.environ.items() if k in ("PATH", "LANG", "LC_ALL", "TERM")},
            "HOME": str(home),
            # What the adapter says it needs, unless the scenario gives the
            # harness's login another way (a Remote Control's run.env).
            **(adapter.env(model_server_url) if base_env is None else base_env),
            **(extra_env or {}),
        }
        self.log_path = log_path
        self._log: IO[bytes] = open(log_path, "wb")
        self._pty_reader: threading.Thread | None = None
        self._master: int | None = None
        argv = command
        if adapter.needs_tty if tty is None else tty:
            master, slave = os.openpty()
            fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", 50, 160, 0, 0))
            env.setdefault("TERM", "xterm-256color")
            self._proc = subprocess.Popen(
                argv, cwd=workspace, env=env, stdin=slave, stdout=slave, stderr=slave, start_new_session=True
            )
            os.close(slave)
            self._master = master
            self._pty_reader = threading.Thread(target=self._copy_pty, args=(master,), daemon=True)
            self._pty_reader.start()
        else:
            self._proc = subprocess.Popen(
                argv,
                cwd=workspace,
                env=env,
                stdout=self._log,
                stderr=subprocess.STDOUT,
                stdin=subprocess.DEVNULL,
                start_new_session=True,
            )

    def _copy_pty(self, master: int) -> None:
        # Until the last process on the terminal is gone (the read then fails).
        try:
            while chunk := os.read(master, 65536):
                self._log.write(chunk)
                self._log.flush()
        except (OSError, ValueError):
            pass
        finally:
            os.close(master)

    def type(self, text: str) -> None:
        """Types a line into a harness running on a terminal, as a person does."""
        assert self._master is not None, "the harness is not on a terminal"
        os.write(self._master, text.encode() + b"\r")

    def output(self) -> str:
        raw = self.log_path.read_bytes()
        # A terminal UI's output is mostly escape sequences; drop them so a failure shows the text.
        text = ANSI.sub(b"", raw).decode(errors="replace")
        return CLUTTER.sub("", text) if self._pty_reader else text

    @property
    def pid(self) -> int:
        return self._proc.pid

    def terminate(self) -> None:
        """SIGTERM to the process itself, as stopping it from its terminal or a service manager does."""
        self._proc.terminate()

    def exit_code(self) -> int | None:
        return self._proc.poll()

    def wait_exit(self, timeout: float | None = None) -> int:
        wait_for("the harness to exit", lambda: self._proc.poll() is not None, timeout=timeout, diagnose=self.output)
        assert self._proc.returncode is not None
        return self._proc.returncode

    def close(self) -> None:
        if self._proc.poll() is None:
            # The group: the harness spawns hooks and MCP helpers.
            os.killpg(self._proc.pid, signal.SIGKILL)
            self._proc.wait()
        if self._pty_reader:
            self._pty_reader.join(timeout=5)
        self._log.close()
