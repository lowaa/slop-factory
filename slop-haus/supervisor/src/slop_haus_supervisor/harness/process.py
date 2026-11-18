"""One harness process and the run it carries: the part that is the same for Claude Code and pi."""

from __future__ import annotations

import json
import os
import queue
import signal
import subprocess
import threading
import uuid
from collections import deque
from collections.abc import Callable, Mapping, Sequence
from dataclasses import replace
from pathlib import Path
from typing import Any, Protocol

from slop_haus_supervisor.harness.base import OutcomeKind, RunOutcome
from slop_haus_supervisor.harness.run_processes import RUN_TOKEN_ENV, RunProcesses
from slop_haus_supervisor.harness.text import SecretMasker, as_dict
from slop_haus_supervisor.text import clean_text

STDERR_LINES = 40
STDERR_DETAIL_CHARS = 600
DRAIN_SECONDS = 2.0
WRITE_FAILURE_GRACE = 2.0


class HarnessProcess:
    """A harness subprocess, spoken to as JSONL on stdin and stdout."""

    def __init__(self, argv: Sequence[str], env: Mapping[str, str], cwd: Path, drain_seconds: float = DRAIN_SECONDS) -> None:
        self._drain_seconds = drain_seconds
        token = uuid.uuid4().hex
        self._tree = RunProcesses(token)
        self._stderr: deque[str] = deque(maxlen=STDERR_LINES)
        self._records: queue.Queue[dict[str, Any] | None] = queue.Queue()
        # Its own process group, so a signal to the group reaches what it started that stayed in
        # it. The model's commands do not (see RunProcesses), and a command left running would go
        # on writing to the working copy under the next session.
        self._proc = subprocess.Popen(
            list(argv), stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, cwd=cwd,
            env={**env, RUN_TOKEN_ENV: token}, text=True, encoding="utf-8", errors="replace", bufsize=1,
            start_new_session=True,
        )
        self._stdout_reader = threading.Thread(target=self._read_stdout, daemon=True)
        self._stderr_reader = threading.Thread(target=self._read_stderr, daemon=True)
        self._stdout_reader.start()
        self._stderr_reader.start()

    def _read_stdout(self) -> None:
        assert self._proc.stdout is not None
        for line in self._proc.stdout:
            line = line.strip()
            try:
                record = json.loads(line) if line else None
            except ValueError:
                continue
            record = as_dict(record)
            if record is not None:
                self._records.put(record)
        self._records.put(None)

    def _read_stderr(self) -> None:
        assert self._proc.stderr is not None
        for line in self._proc.stderr:
            self._stderr.append(line.rstrip())

    def next_record(self) -> dict[str, Any] | None:
        """Blocks for the next record; None once stdout has closed or the process has exited.

        A command the model backgrounded holds the stdout pipe open after the harness is gone, so
        EOF alone could be waited for indefinitely.
        """
        while True:
            try:
                return self._records.get(timeout=0.2)
            except queue.Empty:
                if self._proc.poll() is not None:
                    self._stdout_reader.join(self._drain_seconds)
                    try:
                        return self._records.get_nowait()
                    except queue.Empty:
                        return None

    def exit_code(self) -> int:
        code = self._proc.wait()
        # The last of stderr is still in the pipe when the process exits, and says why.
        self._stderr_reader.join(self._drain_seconds)
        return code

    def stderr_tail(self) -> str:
        return clean_text("\n".join(self._stderr), STDERR_DETAIL_CHARS)

    def write(self, line: str) -> None:
        assert self._proc.stdin is not None
        try:
            self._proc.stdin.write(line + "\n")
            self._proc.stdin.flush()
        except OSError:
            # A harness that refuses at start-up exits without reading stdin, so this write can
            # lose that race; its exit, which the reader sees next, says why. One that closed its
            # stdin but lives on would never produce that exit, so it is ended.
            if self._proc.poll() is None:
                self.terminate(WRITE_FAILURE_GRACE)

    def terminate(self, grace: float) -> None:
        try:
            if self._proc.stdin:
                self._proc.stdin.close()
        except OSError:
            pass
        self._signal_group(signal.SIGTERM)
        self._tree.signal(signal.SIGTERM)
        try:
            self._proc.wait(timeout=grace)
        except subprocess.TimeoutExpired:
            pass
        # Whatever it started that ignored the TERM, or outlived the process.
        self._signal_group(signal.SIGKILL)
        self._proc.wait()
        self._tree.kill_all()

    def _signal_group(self, sig: int) -> None:
        try:
            os.killpg(self._proc.pid, sig)
        except (ProcessLookupError, PermissionError):
            pass


class WireProtocol(Protocol):
    """What one harness's wire protocol decides; ProcessRun owns the threads and the process."""

    started: bool
    session_missing: bool

    def begin(self, write: Callable[[str], None]) -> None:
        """Sends the prompt."""
        ...

    def on_record(self, record: dict[str, Any], write: Callable[[str], None]) -> RunOutcome | None:
        """The run's outcome once this record ends it."""
        ...

    def on_exit(self, code: int, stderr: str) -> RunOutcome:
        """The outcome of a process that exited before the run ended."""
        ...


class ProcessRun:
    """A RunHandle over a HarnessProcess: one prompt, run to its outcome."""

    def __init__(self, process: HarnessProcess, protocol: WireProtocol, session_id: str,
                 grace: float = 5.0, masker: SecretMasker | None = None) -> None:
        self._masker = masker or SecretMasker()
        self._process = process
        self._protocol = protocol
        self._session_id = session_id
        self._grace = grace
        self._lock = threading.Lock()
        self._outcome: RunOutcome | None = None
        self._ended = threading.Event()
        # Set when the protocol has seen the harness start, or the run has ended.
        self._settled = threading.Condition()
        protocol.begin(process.write)
        threading.Thread(target=self._pump, daemon=True).start()

    @property
    def session_id(self) -> str:
        return self._session_id

    @property
    def session_missing(self) -> bool:
        return self._protocol.session_missing

    def await_start(self, timeout: float) -> None:
        """Returns once the harness has started a session or the run has ended, or after `timeout`."""
        with self._settled:
            self._settled.wait_for(lambda: self._protocol.started or self._ended.is_set(), timeout)

    def wait(self, timeout: float) -> RunOutcome | None:
        self._ended.wait(timeout)
        return self._outcome if self._ended.is_set() else None

    def kill(self) -> None:
        self._finish(RunOutcome(OutcomeKind.KILLED, "killed by the supervisor"))

    def _pump(self) -> None:
        try:
            self._pump_records()
        except Exception as e:
            # A record the protocol could not make sense of must not leave wait() blocking for ever.
            self._finish(RunOutcome(OutcomeKind.CRASHED, clean_text(f"the supervisor failed reading the harness: {e!r}", 500)))

    def _pump_records(self) -> None:
        while True:
            record = self._process.next_record()
            if record is None:
                break
            with self._settled:
                outcome = self._protocol.on_record(record, self._process.write)
                self._settled.notify_all()
            if outcome is not None:
                self._finish(outcome)
                return
        code = self._process.exit_code()
        self._finish(self._protocol.on_exit(code, self._process.stderr_tail()))

    def _finish(self, outcome: RunOutcome) -> None:
        with self._lock:
            if self._outcome is not None:
                return
            self._outcome = replace(outcome, detail=self._masker.mask(outcome.detail))
        self._process.terminate(self._grace)
        self._ended.set()
        with self._settled:
            self._settled.notify_all()


class HarnessSession:
    """A multi-turn conversation with one harness process, for a caller that owns the conversation.

    Unlike ProcessRun it ends no turn and classifies nothing: the caller sends messages as it likes
    and reads every raw record in order. close() cleans up the process tree as a run's end does.
    """

    def __init__(self, process: HarnessProcess, encode_user_message: Callable[[str], str], session_id: str,
                 grace: float = 5.0) -> None:
        self._process = process
        self._encode = encode_user_message
        self._session_id = session_id
        self._grace = grace
        self._lock = threading.Lock()
        self._closed = False

    @property
    def session_id(self) -> str:
        return self._session_id

    def send(self, text: str) -> None:
        self._write(self._encode(text))

    def send_line(self, line: str) -> None:
        """A protocol line the harness's own wire protocol needs, apart from a user message."""
        self._write(line)

    def _write(self, line: str) -> None:
        with self._lock:
            if self._closed:
                raise RuntimeError("the session is closed")
        try:
            self._process.write(line)
        except ValueError:
            # close() raced this send and closed the pipe between the check and the write.
            raise RuntimeError("the session is closed") from None

    def interrupt(self) -> bool:
        """Asks the harness to end the turn in flight. True: a native interrupt was sent, and the
        turn will end through the usual records. False: this harness has none; the caller ends the
        process and resumes the session instead."""
        return False

    def next_record(self) -> dict[str, Any] | None:
        """Blocks for the next record; None once the process is gone."""
        return self._process.next_record()

    def exit_code(self) -> int:
        return self._process.exit_code()

    def stderr_tail(self) -> str:
        return self._process.stderr_tail()

    def close(self) -> None:
        with self._lock:
            if self._closed:
                return
            self._closed = True
        self._process.terminate(self._grace)
