from __future__ import annotations

from collections.abc import Callable
from dataclasses import dataclass
from datetime import datetime
from typing import cast

from slop_haus_supervisor.launcher.supervisor_session import age
from slop_haus_supervisor.slophaus.transport import ApiError, Transport

VIEW_PATH = "/api/supervisor/view"


@dataclass(frozen=True)
class BoardSlot:
    agent: str
    phase: str
    reason: str
    reason_detail: str
    ticket_id: int | None
    since: float | None
    offline: bool


@dataclass(frozen=True)
class BoardStatus:
    last_polled_at: float | None
    offline: bool
    slots: tuple[BoardSlot, ...]


def _stamp(value: object) -> float | None:
    return datetime.fromisoformat(value).timestamp() if isinstance(value, str) else None


class BoardView:
    """What the board itself sees of the supervisors, asked with the supervisor's own credential."""

    def __init__(self, transport: Transport, token: Callable[[], str]) -> None:
        self._transport = transport
        self._token = token

    def fetch(self) -> BoardStatus:
        answer = self._transport.send("GET", VIEW_PATH, token=self._token())
        if not answer.ok:
            raise ApiError(f"the board would not show its view of the supervisors: {answer.detail()}", answer.status)
        body = answer.json()
        listed = body.get("slots")
        entries = [cast(dict[str, object], e) for e in cast(list[object], listed) if isinstance(e, dict)] if isinstance(listed, list) else []
        slots = tuple(
            BoardSlot(
                agent=str(raw.get("agent_name") or raw.get("agent_id") or ""),
                phase=str(raw.get("phase") or ""),
                reason=str(raw.get("reason") or "unknown"),
                reason_detail=str(raw.get("reason_detail") or ""),
                ticket_id=ticket if isinstance(ticket := raw.get("ticket_id"), int) else None,
                since=_stamp(raw.get("phase_entered_at")),
                offline=bool(raw.get("offline")),
            )
            for raw in entries
        )
        return BoardStatus(_stamp(body.get("last_polled_at")), bool(body.get("offline")), slots)


def lines(status: BoardStatus, now: float) -> list[str]:
    if status.last_polled_at is None:
        heartbeat = "the board has never seen a supervisor poll"
    else:
        state = "OFFLINE" if status.offline else "online"
        heartbeat = f"last heartbeat {age(now - status.last_polled_at)} ago ({state})"
    out = [f"  board: {heartbeat}"]
    for slot in status.slots:
        ticket = f" ticket=#{slot.ticket_id}" if slot.ticket_id is not None else ""
        reason = ""
        if slot.phase != "running" and slot.reason != "unknown":
            reason = f" reason={slot.reason}" + (f" ({slot.reason_detail})" if slot.reason_detail else "")
        since = f" for {age(now - slot.since)}" if slot.since is not None else ""
        offline = " [offline]" if slot.offline else ""
        out.append(f"  {slot.agent}: {slot.phase}{since}{reason}{ticket}{offline}")
    return out
