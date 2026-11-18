"""Steps the slots and sleeps by what each `step()` says.

One thread per slot: an idle slot's step is a long poll, and a slot waiting on the api must not
hold the others up. The slots share nothing, so the threads need no coordination beyond the
stop event.
"""

from __future__ import annotations

import logging
import threading
import time
from collections.abc import Callable, Sequence
from collections.abc import Mapping
from dataclasses import dataclass
from pathlib import Path
from typing import Protocol, runtime_checkable

from slop_haus_supervisor.fleet.slot import Phase
from slop_haus_supervisor.fleet.slot_report import SlotStateReporter
from slop_haus_supervisor.slophaus.client import SlotSnapshot
from slop_haus_supervisor.service import paused_state
from slop_haus_supervisor.service.control import ControlSpool
from slop_haus_supervisor.slophaus.http_client import ApiStopped
from slop_haus_supervisor.slophaus.transport import ApiError


class Steppable(Protocol):
    agent: str
    directory: Path

    @property
    def phase(self) -> Phase: ...

    def step(self) -> float: ...

    def abandon_run(self) -> None: ...

    def release_lock_for_stop(self) -> None: ...

    def snapshot(self) -> SlotSnapshot: ...

    def pause_info(self) -> tuple[str, float] | None: ...

    def try_unpause(self, manual: bool = False) -> str: ...


@runtime_checkable
class Drainable(Protocol):
    """A slot whose run is a long-lived process that has to be told to stop taking new work (a Remote Control host)."""

    def begin_drain(self) -> None: ...

    def set_held(self, held: bool) -> None: ...


# The longest the steward loop waits between ticks while the server has no steward.
STEWARD_BACKOFF_CAP = 600.0


def backed_off(pause: float) -> float:
    return min(pause * 2, STEWARD_BACKOFF_CAP)


class StewardTicker(Protocol):
    def tick(self) -> object: ...

    def stop(self) -> None: ...


class Reconciler(Protocol):
    def tick(self) -> object: ...


@dataclass
class _Entry:
    """A slot with the thread that steps it, and what the reconciler asked of it."""

    slot: Steppable
    thread: threading.Thread | None = None
    # The owner's hold: no new work is taken, a run in progress finishes.
    held: bool = False
    # Retiring: treated as a drain of this one slot.
    retiring: bool = False


@dataclass(frozen=True)
class SchedulerConfig:
    # An idle slot normally waits inside its poll; this keeps one whose poll comes back at once
    # (a claim that keeps being refused) from spinning.
    idle_gap: float = 1.0
    # A step that raised: the slot is tried again after this, so one bad step cannot end the loop.
    error_backoff: float = 30.0
    # How long a running slot is given to reach a wait point once draining.
    drain_timeout: float = 120.0
    # Extra time past the drain timeout for a slot's thread to notice it and return.
    join_grace: float = 10.0
    # How often the steward is asked whether it is due; a run itself holds its thread until it ends.
    steward_tick: float = 15.0
    # How often the board's desired state is read.
    reconcile_tick: float = 30.0


class LogAlerts:
    def __init__(self, logger: logging.Logger) -> None:
        self._log = logger

    def alert(self, message: str) -> None:
        self._log.error("ALERT: %s", message)


class Scheduler:
    def __init__(
        self,
        slots: Sequence[Steppable],
        stop: threading.Event,
        logger: logging.Logger,
        config: SchedulerConfig = SchedulerConfig(),
        monotonic: Callable[[], float] = time.monotonic,
        reporter: SlotStateReporter | None = None,
        control: ControlSpool | None = None,
        paused_file: Path | None = None,
        steward: StewardTicker | None = None,
        reconciler: Reconciler | None = None,
    ) -> None:
        self._steward = steward
        self._reconciler = reconciler
        # Slots come and go while running, so every reader takes the lock and a copy.
        self._entries_lock = threading.Lock()
        self._entries = {slot.agent: _Entry(slot) for slot in slots}
        self._stop = stop
        self._log = logger
        self._config = config
        self._monotonic = monotonic
        self._reporter = reporter
        self._aborted = threading.Event()
        self._control = control
        self._paused_file = paused_file
        self._paused_written: list[paused_state.PausedSlot] | None = None

    @property
    def reporter(self) -> SlotStateReporter | None:
        return self._reporter

    def drain(self) -> None:
        """No new claims; a running slot is let reach a wait point, then the loop ends."""
        self._stop.set()

    def abort(self) -> None:
        """Drain, and stop waiting for it: running slots are ended now. Safe to call from a signal handler."""
        self._aborted.set()
        self._stop.set()

    @property
    def _slots(self) -> list[Steppable]:
        with self._entries_lock:
            return [entry.slot for entry in self._entries.values()]

    def _start(self, entry: _Entry) -> None:
        entry.thread = threading.Thread(target=self._loop, args=(entry,), name=f"slot-{entry.slot.agent}", daemon=True)
        entry.thread.start()

    def slot_names(self) -> set[str]:
        with self._entries_lock:
            return set(self._entries)

    def add_slot(self, slot: Steppable) -> None:
        """Starts stepping a slot the reconciler provisioned."""
        with self._entries_lock:
            if slot.agent in self._entries:
                return
            entry = self._entries[slot.agent] = _Entry(slot)
        self._start(entry)

    def hold(self, name: str, held: bool) -> None:
        with self._entries_lock:
            entry = self._entries.get(name)
            if entry is None:
                return
            entry.held = held
            if isinstance(entry.slot, Drainable):
                entry.slot.set_held(held)

    def pause_reason(self, name: str) -> str | None:
        """Why the slot paused itself, or None if it has not."""
        with self._entries_lock:
            entry = self._entries.get(name)
        info = entry.slot.pause_info() if entry is not None else None
        return info[0] if info is not None else None

    def retire(self, name: str) -> bool:
        """Stops the slot taking new work; True once its run has ended and its thread has gone, and the slot is forgotten."""
        with self._entries_lock:
            entry = self._entries.get(name)
            if entry is None:
                return True
            entry.retiring = True
            thread = entry.thread
            if thread is not None and thread.is_alive():
                return False
            del self._entries[name]
            return True

    def _threads(self) -> list[threading.Thread]:
        with self._entries_lock:
            return [e.thread for e in self._entries.values() if e.thread is not None]

    def run(self) -> None:
        extra: list[threading.Thread] = []
        if self._steward is not None:
            extra.append(threading.Thread(target=self._steward_loop, name="steward", daemon=True))
        if self._reconciler is not None:
            extra.append(threading.Thread(target=self._reconcile_loop, name="reconcile", daemon=True))
        with self._entries_lock:
            entries = list(self._entries.values())
        for entry in entries:
            self._start(entry)
        for thread in extra:
            thread.start()
        stopped_at: float | None = None
        if self._control is not None:
            self._control.prepare()
            self._control.sweep_stale()
        while alive := [t for t in [*self._threads(), *extra] if t.is_alive()]:
            if self._aborted.is_set():
                self._log.warning("second stop request: ending %d slot(s) now", len(alive))
                self._abandon_runs()
                return
            if self._stop.is_set():
                stopped_at = self._monotonic() if stopped_at is None else stopped_at
                # A step stuck in a call cannot see the deadline; the threads are daemons, so
                # leaving them does not keep the process up.
                if self._monotonic() - stopped_at >= self._config.drain_timeout + self._config.join_grace:
                    self._log.warning("leaving %d slot(s) that did not stop in time", len(alive))
                    self._abandon_runs()
                    return
            self._serve_control()
            alive[0].join(0.2)

    def unpause(self, names: list[str] | None) -> Mapping[str, str]:
        """A person's request: each named slot (all when None), unpaused if its cause allows; "" is done, else why not."""
        by_name = {slot.agent: slot for slot in self._slots}
        wanted = list(by_name) if names is None else names
        answers: dict[str, str] = {}
        for name in wanted:
            slot = by_name.get(name)
            if slot is None:
                answers[name] = "no such slot"
            elif slot.phase != Phase.PAUSED:
                answers[name] = "" if names is None else "not paused"
            else:
                answers[name] = slot.try_unpause(manual=True)
        return answers

    def _serve_control(self) -> None:
        if self._control is not None:
            try:
                self._control.serve_pending(self.unpause)
            except OSError:
                self._log.exception("the control spool could not be served")
        self._publish_paused()

    def _publish_paused(self) -> None:
        if self._paused_file is None:
            return
        paused = [
            paused_state.PausedSlot(slot.agent, info[0], info[1])
            for slot in self._slots
            if (info := slot.pause_info()) is not None
        ]
        if paused == self._paused_written:
            return
        try:
            paused_state.write(self._paused_file, paused)
            self._paused_written = paused
        except OSError:
            self._log.exception("the paused-slot state could not be written")

    def _abandon_runs(self) -> None:
        """Ends every slot's harness process at once; killing one waits out its grace, so not one after another."""
        if self._steward is not None:
            self._steward.stop()
        killers = [threading.Thread(target=slot.abandon_run, daemon=True) for slot in self._slots]
        for killer in killers:
            killer.start()
        for killer in killers:
            killer.join(self._config.join_grace)

    def _steward_loop(self) -> None:
        """The steward is read-only and disposable, so unlike a slot's run it is ended as soon as the loop stops."""
        assert self._steward is not None
        steward = self._steward

        def end_on_stop() -> None:
            self._stop.wait()
            steward.stop()

        threading.Thread(target=end_on_stop, name="steward-stop", daemon=True).start()
        # A server without the steward answers 503 on every tick; retrying at the tick rate would
        # fill the log with the same refusal, so back off and say it once per state change.
        pause = self._config.steward_tick
        unavailable = False
        while not self._stop.is_set():
            try:
                steward.tick()
                if unavailable:
                    self._log.info("steward: the server's steward is available again")
                unavailable = False
                pause = self._config.steward_tick
            except ApiStopped:
                return
            except ApiError as err:
                if err.status != 503:
                    self._log.exception("steward: tick failed; trying again in %.0fs", pause)
                else:
                    if not unavailable:
                        self._log.warning("steward: the server's steward is not available (%s); backing off up to %.0fs", err, STEWARD_BACKOFF_CAP)
                    unavailable = True
                    pause = backed_off(pause)
            except Exception:
                self._log.exception("steward: tick failed; trying again in %.0fs", pause)
            self._stop.wait(pause)

    def _reconcile_loop(self) -> None:
        assert self._reconciler is not None
        last_failure = ""
        while not self._stop.is_set():
            try:
                self._reconciler.tick()
                last_failure = ""
            except ApiStopped:
                return
            except Exception as err:
                # Said once per kind of failure: an unreachable server fails every tick the same way.
                failure = type(err).__name__
                if failure != last_failure:
                    self._log.exception("reconcile: tick failed; trying again in %.0fs", self._config.reconcile_tick)
                last_failure = failure
            self._stop.wait(self._config.reconcile_tick)

    def _loop(self, entry: _Entry) -> None:
        slot = entry.slot
        deadline: float | None = None
        while True:
            if self._stop.is_set() or entry.retiring:
                if deadline is None:
                    deadline = self._monotonic() + self._config.drain_timeout
                    if isinstance(slot, Drainable):
                        slot.begin_drain()
                # A review's processes run in sessions of their own and would outlive this process. The request stays
                # open on the server, so the next supervisor reviews the head again.
                if slot.phase == Phase.WAIT_REVIEW:
                    slot.abandon_run()
                    return
                # A waiting slot keeps its lease on the server, and a later supervisor resumes it.
                if slot.phase != Phase.RUNNING:
                    slot.release_lock_for_stop()
                    return
                if self._monotonic() >= deadline:
                    self._log.warning("%s: still running at the drain timeout; ending the run", slot.agent)
                    slot.abandon_run()
                    return
            if self._aborted.is_set():
                return
            if entry.held and slot.phase in (Phase.IDLE, Phase.PAUSED):
                self._stop.wait(self._config.idle_gap)
                continue
            error: Exception | None = None
            try:
                delay = slot.step()
            except ApiStopped:
                return
            except Exception as err:
                self._log.exception("%s: step failed; trying again in %.0fs", slot.agent, self._config.error_backoff)
                delay = self._config.error_backoff
                error = err
            self._report(slot, error)
            if slot.phase == Phase.IDLE:
                delay = max(delay, self._config.idle_gap)
            if not self._stop.is_set():
                self._stop.wait(delay)

    def _report(self, slot: Steppable, error: Exception | None) -> None:
        if self._reporter is not None:
            self._reporter.after_step(slot, error)
