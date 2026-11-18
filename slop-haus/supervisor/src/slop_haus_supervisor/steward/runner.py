from __future__ import annotations

import logging
import threading
from collections.abc import Sequence
from dataclasses import dataclass
from pathlib import Path
from typing import Protocol

from slop_haus_supervisor.harness.text import SecretMasker
from slop_haus_supervisor.harness.variant import CLAUDE_EFFORT, check_choice, parse_variant
from slop_haus_supervisor.harness.base import Harness, OutcomeKind, RunHandle, RunOutcome, RunSpec
from slop_haus_supervisor.ports import Clock
from slop_haus_supervisor.steward.events import WakeBatcher, WakeEvent
from slop_haus_supervisor.steward.ports import (
    AgreementReports,
    StewardBoard,
    StewardBriefing,
    StewardHarnesses,
    StewardLease,
    StewardModes,
    StewardVariants,
)

STEWARD_AGENT = "steward"


@dataclass(frozen=True)
class StewardConfig:
    # Events within this window of the first share a run.
    batch_window: float = 180.0
    sweep_every: float = 1800.0
    # Wall-clock cap for one run.
    run_timeout: float = 20 * 60.0
    # Passed to the harness as `--max-turns`; see steward_command.
    max_turns: int = 40
    poll_interval: float = 5.0
    # After a usage limit or failed run, wait this long before the next.
    backoff: float = 600.0
    # While the tenant's steward_mode is off, how long until the next look at it.
    off_recheck: float = 1800.0


def steward_command(max_turns: int, base: Sequence[str] = ("claude",)) -> tuple[str, ...]:
    """The ClaudeHarness command for a steward: the turn budget is a CLI flag, so it lives in the command."""
    return (*base, "--max-turns", str(max_turns))


def prompt_for(events: Sequence[WakeEvent], report: str) -> str:
    lines = ["You were woken by these board events:"]
    for e in events:
        ticket = f" ticket #{e.ticket_id}" if e.ticket_id is not None else ""
        lines.append(f"- {e.kind.value}{ticket}" + (f": {e.detail}" if e.detail else ""))
    lines += ["", "Agreement report (read it before proposing):", report or "(none yet)"]
    return "\n".join(lines)


class StewardIdentity(Protocol):
    def ensure(self) -> Path:
        """Makes sure the steward's credential and token helper exist; raises when they cannot be had."""
        ...


class StewardRunner:
    """Runs the tenant's steward as a headless run, one at a time, woken by batched board events and a sweep.

    The run claims no ticket and takes no lock; it can only read, comment and propose (see guard.py).
    `tick` is driven by the supervisor's loop and never blocks longer than one run.
    """

    def __init__(
        self,
        harness: Harness | StewardHarnesses,
        board: StewardBoard,
        briefing: StewardBriefing,
        reports: AgreementReports,
        lease: StewardLease,
        clock: Clock,
        scratch: Path,
        logger: logging.Logger,
        config: StewardConfig = StewardConfig(),
        identity: StewardIdentity | None = None,
        masker: SecretMasker | None = None,
        modes: StewardModes | None = None,
        variants: StewardVariants | None = None,
    ) -> None:
        self._variants = variants
        self._masker = masker or SecretMasker()
        self._identity = identity
        self._modes = modes
        self._stopped = threading.Event()
        self._handle: RunHandle | None = None
        self._harness = harness
        self._board = board
        self._briefing = briefing
        self._reports = reports
        self._lease = lease
        self._clock = clock
        self._scratch = scratch
        self._log = logger
        self._config = config
        self._batcher = WakeBatcher(config.batch_window, config.sweep_every, clock.now())
        self._not_before = 0.0

    def tick(self) -> RunOutcome | None:
        """Feeds new events in and runs the steward if a batch is due; the run's outcome, or None."""
        if self._stopped.is_set():
            return None
        now = self._clock.now()
        for event in self._board.new_events():
            self._batcher.add(event, now)
        if now < self._not_before:
            return None
        batch = self._batcher.take(now)
        if batch is None:
            return None
        if not self._enabled(batch, now):
            return None
        if not self._lease.acquire():
            # Another supervisor's steward is running; the events are not lost to it, they are on the board.
            self._log.info("steward: another run holds the tenant's steward lease")
            self._batcher.requeue(batch, now)
            return None
        try:
            return self._run(batch)
        except Exception:
            # The run could not start (the harness raised); the events must not wait for the next sweep.
            self._batcher.requeue(list(batch), self._clock.now())
            raise
        finally:
            self._lease.release()

    def _enabled(self, batch: Sequence[WakeEvent], now: float) -> bool:
        """False while steward_mode is off, or cannot be read: no enrolment, no lease, no run.

        The batch is kept for when it is switched on, and the mode is looked at again only after `off_recheck`
        so an off tenant costs one cheap call per period, not one per tick.
        """
        if self._modes is None:
            return True
        try:
            mode = self._modes.mode()
        except Exception as err:  # noqa: BLE001 -- unknown is not on: the next look decides
            self._log.warning("steward: no run, could not read steward_mode: %s", self._masker.mask(f"{type(err).__name__}: {err}"))
            wait = self._config.backoff
        else:
            if mode != "off":
                return True
            self._log.info("steward: steward_mode is off; not running")
            wait = self._config.off_recheck
        self._not_before = now + wait
        self._batcher.requeue(list(batch), now)
        return False

    def stop(self) -> None:
        """Ends the run in progress, if any, and runs no more. Safe to call from another thread."""
        self._stopped.set()
        handle = self._handle
        if handle is not None:
            handle.kill()

    def _variant(self) -> str:
        """The model and effort for this run, checked as a worker's variant is; an invalid one raises InvalidVariant, which `_run` treats as a failure to brief."""
        variant = self._variants.variant() if self._variants is not None else ""
        check_choice("effort", parse_variant(variant).effort, CLAUDE_EFFORT)
        return variant

    def _run(self, batch: Sequence[WakeEvent]) -> RunOutcome | None:
        try:
            if self._identity is not None:
                self._identity.ensure()
            system = self._briefing.load()
            prompt = prompt_for(batch, self._reports.agreement_report())
            # Before the variant: which harness runs decides which settings the variant reads.
            harness = self._harness.current() if isinstance(self._harness, StewardHarnesses) else self._harness
            variant = self._variant()
        except Exception as err:  # noqa: BLE001 -- any failure to brief means no run, and the next sweep tries again
            self._log.warning("steward: no run, could not brief it: %s", self._masker.mask(f"{type(err).__name__}: {err}"))
            self._not_before = self._clock.now() + self._config.backoff
            self._batcher.requeue(list(batch), self._clock.now())
            return None
        self._scratch.mkdir(parents=True, exist_ok=True)
        spec = RunSpec(STEWARD_AGENT, self._scratch, 0, "", prompt, variant=variant, system_prompt=system)
        handle = harness.start(spec)
        self._handle = handle
        deadline = self._clock.now() + self._config.run_timeout
        outcome: RunOutcome | None = None
        while outcome is None:
            if self._stopped.is_set():
                handle.kill()
                outcome = RunOutcome(OutcomeKind.KILLED, "the supervisor is stopping")
                break
            if self._clock.now() >= deadline:
                handle.kill()
                outcome = RunOutcome(OutcomeKind.OVER_BUDGET, "the steward run passed its time budget")
                break
            outcome = handle.wait(self._config.poll_interval)
        self._handle = None
        if outcome.kind is not OutcomeKind.FINISHED:
            self._log.warning("steward: run ended %s", outcome.kind.value)
            self._not_before = self._clock.now() + self._config.backoff
        return outcome
