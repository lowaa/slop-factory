from enum import StrEnum


class Phase(StrEnum):
    IDLE = "idle"
    RUNNING = "running"
    WAIT_LOCK = "wait_lock"
    WAIT_CHECKS = "wait_checks"
    WAIT_REVIEW = "wait_review"
    BACKOFF = "backoff"
    WAIT_RESET = "wait_reset"
    PAUSED = "paused"


class Reason(StrEnum):
    """Why a slot that is not running a ticket is not; sent beside the phase so an operator need not read logs."""

    NO_CLAIMABLE = "no_claimable"
    FLEET_PAUSED = "fleet_paused"
    SLOT_PAUSED = "slot_paused"
    LOCK_QUEUED = "lock_queued"
    COOLDOWN = "cooldown"
    BACKOFF_AFTER_ERROR = "backoff_after_error"
    WAITING_CI = "waiting_ci"
    REVIEWING = "reviewing"
    UPDATING = "updating"
    STARTING = "starting"
    OTHER = "other"
