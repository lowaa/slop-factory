from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum
from typing import Any, Protocol

from slop_haus_supervisor.harness.base import Usage


class EventKind(StrEnum):
    CLAIMABLE = "claimable"
    MESSAGE = "message"
    LEASE_REVOKED = "lease_revoked"
    LOCK_OFFER = "lock_offer"
    RESTART = "restart"
    DISABLED = "disabled"


@dataclass(frozen=True)
class Event:
    kind: EventKind
    ticket_id: int | None = None


class LockState(StrEnum):
    NONE = "none"
    QUEUED = "queued"
    OFFERED = "offered"
    HELD = "held"


class ChecksState(StrEnum):
    NONE = "none"
    PENDING = "pending"
    PASSED = "passed"
    FAILED = "failed"


@dataclass(frozen=True)
class ReviewRequest:
    """The agent's open request for the supervisor to review a pushed head; the server closes it once the runs are in."""

    head_sha: str
    # The change's description, as the agent wrote it; handed to the reviewers as the author's claim.
    intent: str
    requested_at: str


@dataclass(frozen=True)
class WorkerState:
    lease_held: bool
    ticket_status: str = ""
    lock: LockState = LockState.NONE
    checks: ChecksState = ChecksState.NONE
    # The held lease's ticket and attempt, so a restarted slot can take it up again.
    ticket_id: int | None = None
    attempt: int | None = None
    # The held ticket's path scopes, when the server's answer carries them.
    paths: tuple[str, ...] = ()
    # Only a session this harness can resume.
    session_id: str | None = None
    review_request: ReviewRequest | None = None


@dataclass(frozen=True)
class Claim:
    ticket_id: int
    branch: str
    base_commit: str
    variant: str = ""
    # A ticket without paths (planning, human steps) gets no attempt branch from the server, so its run has nothing to commit to.
    has_paths: bool = True
    # The ticket's path scopes, from which the run's repo is found.
    paths: tuple[str, ...] = ()
    # Set when this agent is picking its own attempt up again (after a discussion answer).
    session_id: str | None = None


class ClaimRefused(Exception):
    """Someone else got the ticket first; the slot stays idle."""


@dataclass(frozen=True)
class SlotSnapshot:
    """What a slot says about itself. `phase` is a fleet.phase.Phase value.

    `error_class` is only ever a short token for an exception type: a message can carry a
    ticket's text or a credential, so none travels. `pause_reason` is the one free-text field: it is
    already masked and bounded by the slot, and the server bounds it again.
    """

    phase: str
    phase_age_seconds: float
    ticket_id: int | None = None
    error_class: str = ""
    error_streak: int = 0
    pause_reason: str = ""
    # A fleet.phase.Reason value; empty while the slot runs a ticket.
    reason: str = ""
    # A short note beside the reason (bounded by the slot, and again by the server).
    reason_detail: str = ""
    # The workspace layout (workspace/layout_report.py), sent when the reporter has a fresh one.
    layout: dict[str, Any] | None = None
    # The managed asset bundle hash the slot's workspace installed (workspace/bundle.py); empty when it has none.
    bundle_hash: str = ""
    # No live session: the slot may be restarted or its workspace synced. Only a Remote Control host says so.
    idle: bool = False


class SlopHaus(Protocol):
    @property
    def default_variant(self) -> str: ...

    def poll(self, agent: str, timeout: float) -> list[Event]: ...

    def claim(self, agent: str, ticket_id: int | None = None, bundle_hash: str = "") -> Claim: ...

    def heartbeat(self, agent: str) -> None: ...

    def state(self, agent: str) -> WorkerState: ...

    def take_lock(self, agent: str) -> None: ...

    def release_lock(self, agent: str) -> None: ...

    def record_session(self, agent: str, harness: str, session_id: str) -> None: ...

    def escalate(self, agent: str, note: str) -> None: ...

    def release(self, agent: str, reason: str) -> None:
        """Hands the held ticket back to the queue, for any agent to claim; unlike escalate, no person is asked."""
        ...

    def report_usage(self, agent: str, usage: Usage) -> None: ...

    def report_slot_state(self, agent: str, snapshot: SlotSnapshot) -> None:
        """Best effort: raises when the report was not taken, and the caller carries on."""
        ...
