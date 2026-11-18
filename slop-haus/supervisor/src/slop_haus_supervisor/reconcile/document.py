"""The desired-state document as the supervisor reads it.

An entry that is not exactly what the server documents is set aside with a reason, never repaired:
the document is the one input here that a person on the board writes, so nothing in it is trusted
to be a path or a command. Names are checked where a directory is derived from them (`Layout`).
"""

from __future__ import annotations

import uuid
from dataclasses import dataclass, field
from typing import cast

from slop_haus_supervisor.workspace.provisioned import Kind

STATES = ("running", "paused", "removed")
LAUNCH_SUPERVISOR = "supervisor"
LAUNCHES = (LAUNCH_SUPERVISOR, "manual")
# `observed` is the supervisor's own earlier report, echoed back. `launch` is the server's word on who runs
# a Remote Control host; the server leaves it out for the other kinds.
_FIELDS = frozenset({"id", "kind", "name", "harness", "state", "launch", "generation", "resume_generation", "observed"})


@dataclass(frozen=True)
class DesiredAgent:
    id: str
    kind: Kind
    name: str
    harness: str
    state: str
    generation: int
    resume_generation: int
    # Only a Remote Control entry has one; a server that says nothing means the owner runs the host.
    launch: str = "manual"

    @property
    def supervisor_launched(self) -> bool:
        return self.kind is Kind.REMOTE_CONTROL and self.launch == LAUNCH_SUPERVISOR

    @property
    def removed(self) -> bool:
        return self.state == "removed"


@dataclass(frozen=True)
class Rejected:
    """An entry that could not be read. `id` is None when even that is unusable, so it cannot be reported on."""

    id: str | None
    generation: int
    reason: str


@dataclass(frozen=True)
class Document:
    revision: int
    agents: tuple[DesiredAgent, ...] = ()
    rejected: tuple[Rejected, ...] = field(default_factory=tuple)


def _int(value: object) -> int | None:
    return value if isinstance(value, int) and not isinstance(value, bool) and value >= 0 else None


def _entry_id(fields: dict[str, object]) -> str | None:
    raw = fields.get("id")
    try:
        return str(uuid.UUID(raw)) if isinstance(raw, str) else None
    except ValueError:
        return None


def _agent(fields: dict[str, object]) -> DesiredAgent | str:
    """The entry, or why it is refused."""
    extra = sorted(set(fields) - _FIELDS)
    if extra:
        return f"unexpected field {extra[0]!r}: the document carries names and kinds only"
    entry_id = _entry_id(fields)
    kind, name, harness, state = fields.get("kind"), fields.get("name"), fields.get("harness", "claude"), fields.get("state")
    generation, resume = _int(fields.get("generation")), _int(fields.get("resume_generation", 0))
    if entry_id is None:
        return "the entry has no valid id"
    if kind not in {k.value for k in Kind}:
        return "unknown kind"
    if not isinstance(name, str) or not name:
        return "the entry has no name"
    if not isinstance(harness, str) or not harness:
        return "the entry has no harness"
    if state not in STATES:
        return "unknown state"
    if generation is None or resume is None:
        return "generation and resume_generation must be whole numbers"
    launch = fields.get("launch", "manual")
    if launch == "":
        launch = "manual"
    if launch not in LAUNCHES:
        return "unknown launch"
    return DesiredAgent(entry_id, Kind(cast(str, kind)), name, harness, cast(str, state), generation, resume, cast(str, launch))


def parse(raw: dict[str, object]) -> Document:
    revision, listed = _int(raw.get("revision")), raw.get("agents")
    if revision is None or not isinstance(listed, list):
        raise ValueError("the desired-state document is not recognisable")
    agents: list[DesiredAgent] = []
    rejected: list[Rejected] = []
    names: set[str] = set()
    for entry in cast(list[object], listed):
        fields = cast(dict[str, object], entry) if isinstance(entry, dict) else {}
        parsed = _agent(fields) if fields else "the entry is not an object"
        if isinstance(parsed, str):
            rejected.append(Rejected(_entry_id(fields), _int(fields.get("generation")) or 0, parsed))
        elif parsed.name in names:
            rejected.append(Rejected(parsed.id, parsed.generation, f"the name {parsed.name!r} appears twice"))
        else:
            names.add(parsed.name)
            agents.append(parsed)
    return Document(revision, tuple(agents), tuple(rejected))
