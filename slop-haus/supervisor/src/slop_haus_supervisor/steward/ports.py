"""What the steward runner needs from the server, as seams; the HTTP adapters are wired separately."""

from __future__ import annotations

from collections.abc import Sequence
from typing import Protocol, runtime_checkable

from slop_haus_supervisor.harness.base import Harness

from slop_haus_supervisor.steward.events import WakeEvent


class StewardBoard(Protocol):
    def new_events(self) -> Sequence[WakeEvent]:
        """Board events since the last call that should wake the steward (a ticket entering discussion, an agent-filed preparing ticket)."""
        ...


class StewardBriefing(Protocol):
    def load(self) -> str:
        """The server's steward briefing, the run's system prompt; raises when it cannot be had."""
        ...


class AgreementReports(Protocol):
    def agreement_report(self) -> str:
        """The agreement report text (the server's agreement report) the run starts from."""
        ...


class StewardLease(Protocol):
    def acquire(self) -> bool:
        """Takes the tenant's single steward slot; False when another supervisor's run holds it."""
        ...

    def release(self) -> None: ...


class StewardModes(Protocol):
    def mode(self) -> str:
        """The tenant's steward_mode (off, shadow or live); raises when it cannot be had."""
        ...


class StewardVariants(Protocol):
    def variant(self) -> str:
        """The variant text a steward run starts under (`model=x,effort=high`); empty for the harness's own defaults.

        Never raises for a config that cannot be read: it falls back, and says why in the log.
        """
        ...


@runtime_checkable
class StewardHarnesses(Protocol):
    def current(self) -> Harness:
        """The harness this run of the steward starts on, as the owner's harness config now says.

        Raises when the steward must not run on it (an unsafe Codex sandbox, a harness with no read-only steward): no run starts,
        and the reason has already been reported to the owner.
        """
        ...


class StewardStatus(Protocol):
    def report(self, phase: str, reason: str = "") -> None:
        """Tells the board the steward's state (`running` or `failed`, with why); never raises."""
        ...
