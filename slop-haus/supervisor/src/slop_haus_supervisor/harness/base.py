from __future__ import annotations

from collections.abc import Callable, Mapping
from dataclasses import dataclass, field
from enum import StrEnum
from pathlib import Path
from typing import Protocol


# What one agent's runs get on top of the host's environment: its layered secrets and run.env.
AgentEnv = Callable[[str], Mapping[str, str]]


class OutcomeKind(StrEnum):
    FINISHED = "finished"
    USAGE_LIMIT = "usage_limit"
    AUTH_FAILED = "auth_failed"
    PERMISSION_REFUSED = "permission_refused"
    TRANSIENT = "transient"
    CRASHED = "crashed"
    OVER_BUDGET = "over_budget"
    KILLED = "killed"


@dataclass(frozen=True)
class ModelUsage:
    """What one run spent on one model, named as the harness names it so the server can price it."""

    provider: str
    model: str
    billing_mode: str
    # Keyed as usage.conversation.COUNTERS: requests, input, output, cache_read, cache_write, reasoning, cost_micros.
    counters: Mapping[str, int]


@dataclass(frozen=True)
class Usage:
    """A run's usage, per model: a subagent on another model is its own entry."""

    models: tuple[ModelUsage, ...] = ()


@dataclass(frozen=True)
class RunOutcome:
    kind: OutcomeKind
    detail: str = ""
    usage: Usage = field(default_factory=Usage)
    # Absolute epoch seconds; only meaningful for USAGE_LIMIT, and only when the harness reports one.
    reset_at: float | None = None


@dataclass(frozen=True)
class RunSpec:
    agent: str
    directory: Path
    ticket_id: int
    branch: str
    prompt: str
    # The ticket's variant (model, effort); the harness decides what it means.
    variant: str = ""
    # Appended to the harness's system prompt; empty means the harness's own.
    system_prompt: str = ""
    # The slot's workspace root, when the run is started in a repo below it.
    workspace: Path | None = None


@dataclass(frozen=True)
class LaunchPlan:
    """Everything needed to start one harness process, for a caller that owns the conversation itself."""

    argv: list[str]
    env: dict[str, str]
    cwd: Path


class SessionNotFound(Exception):
    """resume() found no such conversation; the caller starts a fresh session."""


class RunHandle(Protocol):
    @property
    def session_id(self) -> str: ...

    def wait(self, timeout: float) -> RunOutcome | None:
        """Block up to `timeout` seconds; None means the run is still alive.

        A timeout, rather than a blocking wait, lets the slot heartbeat and watch its
        budget while a run is alive without a thread per run.
        """
        ...

    def kill(self) -> None: ...


class Harness(Protocol):
    @property
    def name(self) -> str: ...

    def check_installed(self) -> None: ...

    def config_dir(self, root: Path) -> Path: ...

    def start(self, spec: RunSpec) -> RunHandle: ...

    def resume(self, spec: RunSpec, session_id: str) -> RunHandle:
        """Raises SessionNotFound when the conversation is gone."""
        ...


class ConfiguredHarness(Harness, Protocol):
    """A harness the factory built, from the root's secrets and config."""

    def missing_credentials(self) -> str:
        """What to set in the root's secrets.env for runs to log in, or "" when they can."""
        ...
