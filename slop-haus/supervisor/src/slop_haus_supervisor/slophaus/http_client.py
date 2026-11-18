"""The `SlopHaus` port over the server's REST routes, as the slot's own agent.

An api outage is the failure the supervisor exists to ride out, so a call that cannot get an
answer (no connection, 5xx, 429) waits and tries again until it
does, and gives up only when the supervisor is shutting down. A token the server refuses is
replaced at once; refusing the replacement too raises AuthRejected.
"""

from __future__ import annotations

import http.client
import json
import logging
import threading
import urllib.error
import urllib.request
from collections.abc import Callable, Mapping
from dataclasses import dataclass
from datetime import UTC, datetime
from typing import Any, cast

from slop_haus_supervisor.net import opener_without_redirects
from slop_haus_supervisor.harness.base import Usage
from slop_haus_supervisor.ports import Clock, SystemClock
from slop_haus_supervisor.slophaus.client import (
    ChecksState,
    Claim,
    ClaimRefused,
    Event,
    EventKind,
    LockState,
    ReviewRequest,
    SlotSnapshot,
    WorkerState,
)
from slop_haus_supervisor.slophaus.token import SlotToken, TokenUnavailable
from slop_haus_supervisor.usage.conversation import USAGE_REVISION, ConversationUsage

# What a failure of the server's end looks like from here; the request may be repeated later.
RETRY_STATUSES = frozenset({408, 425, 429})
# What an old pod answers mid-rollout for a route it lacks (404) or a rule it predates (403). The
# routes this client calls all exist, so the answer is repeated a few times, long enough to cover a
# rollout, before it is taken as the server's real refusal.
ROLLOUT_STATUSES = frozenset({403, 404})
ROLLOUT_ATTEMPTS = 6
# The protocol this supervisor speaks to the server, sent on every request as X-Supervisor-Version and in
# each poll's body. Raise it, with the server's wakeup.SupervisorProtocol, when the poll or slot-state
# contract changes in a way a compatibility check on older supervisors will need to key on.
SUPERVISOR_PROTOCOL = 1
# Past the poll's own wait, for the response to arrive.
POLL_SLACK = 20.0
# A fixed label: which credential a host holds is a secret's metadata, which stays off the server.
BILLING_MODE = "subscription"

EVENT_KINDS = {
    "claimable": EventKind.CLAIMABLE,
    "messages": EventKind.MESSAGE,
    "revoked": EventKind.LEASE_REVOKED,
    "finished": EventKind.LEASE_REVOKED,
    "lease_lost": EventKind.LEASE_REVOKED,
    "offered": EventKind.LOCK_OFFER,
    "restart": EventKind.RESTART,
}
CHECKS = {
    "success": ChecksState.PASSED,
    "failure": ChecksState.FAILED,
    "pending": ChecksState.PENDING,
    "none": ChecksState.NONE,
    # The code host could not be asked: looking again later is better than nudging the model.
    "unknown": ChecksState.PENDING,
}


class ApiStopped(Exception):
    """The supervisor is shutting down; the call was abandoned."""


class ApiError(Exception):
    def __init__(self, status: int, detail: str) -> None:
        super().__init__(f"{status}: {detail}")
        self.status = status
        self.detail = detail


class AuthRejected(ApiError):
    """The server refused a freshly fetched token: the agent is revoked or disabled, not just stale."""

    def __init__(self, detail: str) -> None:
        super().__init__(401, detail)


@dataclass(frozen=True)
class Backoff:
    first: float = 1.0
    cap: float = 30.0

    def delay(self, attempt: int) -> float:
        return min(self.cap, self.first * (2 ** min(attempt, 16)))


@dataclass
class _Held:
    """What is known of the lease an agent holds, which the poll needs to hear it end."""

    holds: bool = False
    lease_id: int | None = None
    ticket_id: int | None = None


@dataclass
class _SessionUsage:
    harness: str
    session_id: str
    totals: ConversationUsage


def _object(value: object) -> dict[str, Any]:
    return cast(dict[str, Any], value) if isinstance(value, dict) else {}


def _int(value: object) -> int | None:
    return value if isinstance(value, int) else None


def _paths(ticket: dict[str, Any]) -> tuple[str, ...]:
    """A ticket's path scopes: the claim lists them as {path, source} objects, the worker state as plain strings."""
    paths = ticket.get("paths")
    if not isinstance(paths, list):
        return ()
    found: list[object] = [_object(cast(object, x)).get("path") if isinstance(x, dict) else x for x in cast(list[object], paths)]
    return tuple(x for x in found if isinstance(x, str))


def _review_request(value: object) -> ReviewRequest | None:
    """None for no request, and for one without a head: there is nothing to review then."""
    request = _object(value)
    head, intent, at = request.get("head_sha"), request.get("intent"), request.get("requested_at")
    if not isinstance(head, str) or not head:
        return None
    return ReviewRequest(head, intent if isinstance(intent, str) else "", at if isinstance(at, str) else "")


class HttpSlopHaus:
    def __init__(
        self,
        server: str,
        tokens: Mapping[str, SlotToken],
        harness: str,
        logger: logging.Logger,
        *,
        version: str = "",
        harness_version: str = "",
        default_variant: str = "",
        backoff: Backoff = Backoff(),
        rollout_attempts: int = ROLLOUT_ATTEMPTS,
        pause: Callable[[float], bool] | None = None,
        on_restart: Callable[[], None] | None = None,
        clock: Clock | None = None,
    ) -> None:
        self._server = server.rstrip("/")
        self._tokens = tokens
        self._harness = harness
        self._log = logger
        self._version = version
        self._harness_version = harness_version
        # The server names no variant on a claim yet, so the tenant's default applies.
        self._default_variant = default_variant
        self._backoff = backoff
        self._rollout_attempts = rollout_attempts
        # Waits, and says whether the supervisor was told to stop meanwhile.
        self._pause = pause or threading.Event().wait
        self._on_restart = on_restart or (lambda: None)
        self._clock = clock or SystemClock()
        self._opener = opener_without_redirects()
        self._lock = threading.Lock()
        self._held: dict[str, _Held] = {}
        self._usage: dict[str, _SessionUsage] = {}
        self._warned_disabled = False
        self._warned_protocol: str | None = None

    # -- transport --

    def _send(self, token: str, method: str, path: str, body: object | None, timeout: float) -> object:
        headers = {"Authorization": f"Bearer {token}", "X-Supervisor-Version": str(SUPERVISOR_PROTOCOL)}
        data = None
        if body is not None:
            data = json.dumps(body).encode()
            headers["Content-Type"] = "application/json"
        request = urllib.request.Request(self._server + path, data=data, method=method, headers=headers)
        with self._opener.open(request, timeout=timeout) as response:
            raw = response.read()
        return json.loads(raw) if raw else {}

    @staticmethod
    def _detail(err: urllib.error.HTTPError) -> str:
        try:
            raw = err.read()
        except OSError:
            return str(err.reason)
        try:
            detail = _object(json.loads(raw)).get("detail")
        except ValueError:
            detail = None
        return detail if isinstance(detail, str) else raw.decode(errors="replace")[:200]

    def _call(
        self,
        agent: str,
        method: str,
        path: str,
        body: object | None = None,
        timeout: float = 30.0,
        attempts: int | None = None,
    ) -> dict[str, Any]:
        """Returns the response; raises ApiError for an answer that repeating will not change.

        403 and 404 are repeated only `rollout_attempts` times; 409 and 422 are final at once.

        `attempts` bounds the tries for calls nothing should wait on; None means until it works.
        """
        attempt = 0
        rollout = 0
        rejected = ""
        while True:
            reason: str
            token = ""
            try:
                token = self._tokens[agent].get(rejected)
                return _object(self._send(token, method, path, body, timeout))
            except urllib.error.HTTPError as err:
                if err.code == 401:
                    # An expired token is routine (another thread's call, or a revoked-then-reissued
                    # one): replace it and go again at once. Being refused the replacement is real.
                    if rejected:
                        self._log.error("%s: %s %s: the server rejects a freshly fetched token", agent, method, path)
                        raise AuthRejected(self._detail(err)) from err
                    rejected = token
                    self._log.debug("%s: %s %s: token refused; fetching another", agent, method, path)
                    continue
                rejected = ""
                if err.code in ROLLOUT_STATUSES:
                    rollout += 1
                    if rollout >= self._rollout_attempts:
                        raise ApiError(err.code, self._detail(err)) from err
                elif not (err.code in RETRY_STATUSES or err.code >= 500):
                    raise ApiError(err.code, self._detail(err)) from err
                reason = f"{err.code} {self._detail(err)}".strip()
            except (OSError, http.client.HTTPException, ValueError, TokenUnavailable) as err:
                rejected = ""
                reason = str(err) or type(err).__name__
            attempt += 1
            if attempts is not None and attempt >= attempts:
                raise ApiError(0, reason)
            delay = self._backoff.delay(attempt - 1)
            self._log.warning("%s %s: %s; trying again in %.0fs", method, path, reason, delay)
            if self._pause(delay):
                raise ApiStopped()

    def _held_for(self, agent: str) -> _Held:
        with self._lock:
            return self._held.setdefault(agent, _Held())

    # -- SlopHaus --

    def poll(self, agent: str, timeout: float) -> list[Event]:
        held = self._held_for(agent)
        if held.holds and held.lease_id is None:
            self.state(agent)
        body: dict[str, object] = {"wait_seconds": int(timeout), "protocol": SUPERVISOR_PROTOCOL}
        if self._version:
            body["version"] = self._version
        if held.holds and held.lease_id is not None:
            body["lease_id"] = held.lease_id
        answer = self._call(agent, "POST", "/api/worker/poll", body, timeout + POLL_SLACK)
        events: list[Event] = []
        for raw in cast(list[object], answer.get("events") or []):
            item = _object(raw)
            kind = EVENT_KINDS.get(str(item.get("kind")))
            if kind is None:
                self._note_unmapped(str(item.get("kind")), timeout, str(item.get("message") or ""))
                if item.get("kind") == "disabled":
                    events.append(Event(EventKind.DISABLED))
                continue
            if kind == EventKind.LEASE_REVOKED:
                ended = _int(item.get("lease_id"))
                if ended is not None and held.lease_id is not None and ended != held.lease_id:
                    continue
                held.holds, held.lease_id = False, None
            if kind == EventKind.RESTART:
                self._on_restart()
            events.append(Event(kind, _int(item.get("ticket_id"))))
        return events

    def _note_unmapped(self, kind: str, timeout: float, message: str = "") -> None:
        if kind == "protocol_unsupported":
            # Once per message: the poll is answered at once, so it would otherwise log every few seconds.
            if message != self._warned_protocol:
                self._warned_protocol = message
                self._log.error("%s", message or "the server no longer supports this supervisor's protocol; run `slop-haus update`")
            if self._pause(timeout):
                raise ApiStopped()
            return
        if kind != "disabled":
            return
        if not self._warned_disabled:
            self._warned_disabled = True
            self._log.warning("the server has ticket processing turned off; polling slowly")
        # The poll answers a disabled server at once, so waiting is left to us.
        if self._pause(timeout):
            raise ApiStopped()

    @property
    def default_variant(self) -> str:
        return self._default_variant

    def claim(self, agent: str, ticket_id: int | None = None, bundle_hash: str = "") -> Claim:
        body: dict[str, object] = {"harness": self._harness}
        # Recorded with the lease; a server that predates them ignores both.
        if self._harness_version:
            body["harness_version"] = self._harness_version
        if bundle_hash:
            body["bundle_hash"] = bundle_hash
        if ticket_id is not None:
            body["ticket_id"] = ticket_id
        try:
            answer = self._call(agent, "POST", "/api/claims", body)
        except ApiError as err:
            if err.status == 409:
                raise ClaimRefused(err.detail) from err
            raise
        ticket = _object(answer.get("ticket"))
        if not ticket:
            raise ClaimRefused("nothing to claim")
        attempt = _object(answer.get("attempt"))
        claimed = _int(ticket.get("id"))
        branch = attempt.get("branch")
        if claimed is None or not isinstance(branch, str):
            raise ApiError(0, "the claim response names no ticket or branch")
        held = self._held_for(agent)
        held.holds, held.lease_id, held.ticket_id = True, None, claimed
        session = attempt.get("harness_session_id")
        # Another harness's session cannot be resumed by this one.
        resumable = isinstance(session, str) and session and attempt.get("harness") == self._harness
        return Claim(
            ticket_id=claimed,
            branch=branch,
            base_commit=str(attempt.get("base_commit") or ""),
            variant=self._default_variant,
            has_paths=bool(ticket.get("paths")),
            paths=_paths(ticket),
            session_id=session if resumable and isinstance(session, str) else None,
        )

    def heartbeat(self, agent: str) -> None:
        try:
            self._call(agent, "POST", "/api/claims/heartbeat", {})
        except ApiError as err:
            # The lease is gone; the next look at the state says so.
            self._log.warning("%s: heartbeat refused: %s", agent, err)

    def state(self, agent: str) -> WorkerState:
        answer = self._call(agent, "GET", "/api/worker/state")
        lease = _object(answer.get("lease"))
        held = self._held_for(agent)
        held.holds = bool(lease)
        held.lease_id = _int(lease.get("id")) if lease else None
        ticket = _object(answer.get("ticket"))
        lock = _object(answer.get("lock"))
        checks = _object(answer.get("checks"))
        if lock.get("holds"):
            lock_state = LockState.HELD
        elif lock.get("offered"):
            lock_state = LockState.OFFERED
        elif (_int(lock.get("position")) or 0) > 0:
            lock_state = LockState.QUEUED
        else:
            lock_state = LockState.NONE
        attempt = _object(answer.get("attempt"))
        session = attempt.get("harness_session_id")
        resumable = isinstance(session, str) and session and attempt.get("harness") == self._harness
        return WorkerState(
            lease_held=bool(lease),
            ticket_status=str(ticket.get("status") or ""),
            lock=lock_state,
            checks=CHECKS.get(str(checks.get("state")), ChecksState.NONE),
            ticket_id=_int(lease.get("ticket_id")) if lease else None,
            attempt=_int(lease.get("attempt")) if lease else None,
            paths=_paths(ticket),
            session_id=session if resumable and isinstance(session, str) else None,
            review_request=_review_request(answer.get("review_request")) if lease else None,
        )

    def take_lock(self, agent: str) -> None:
        self._call(agent, "POST", "/api/integration_lock", {})

    def release_lock(self, agent: str) -> None:
        try:
            self._call(agent, "DELETE", "/api/integration_lock")
        except ApiError as err:
            # Not held any more is what releasing wanted.
            if err.status != 409 and err.status != 404:
                raise
            self._log.warning("%s: integration unlock found nothing to release: %s", agent, err)

    def record_session(self, agent: str, harness: str, session_id: str) -> None:
        started = datetime.fromtimestamp(self._clock.now(), UTC).strftime("%Y-%m-%dT%H:%M:%SZ")
        self._usage[agent] = _SessionUsage(harness, session_id, ConversationUsage({"started_at": started}))
        try:
            self._call(agent, "POST", "/api/claims/session", {"harness": harness, "session_id": session_id})
        except ApiError as err:
            # Only a later resume loses by this, and it falls back to a fresh session.
            self._log.warning("%s: could not record session %s: %s", agent, session_id, err)

    def escalate(self, agent: str, note: str) -> None:
        held = self._held_for(agent)
        try:
            self._call(agent, "POST", "/api/claims/escalate", {"note": note})
        except ApiError as err:
            if err.status != 409:
                raise
            # The lease was already gone, which is what escalating wanted. A ticket comment
            # needs a user token, so the reason is logged here or nowhere.
            self._log.warning(
                "%s: escalate found no lease (ticket #%s already handed over); supervisor's reason: %s (%s)",
                agent, held.ticket_id, note, err,
            )
        held.holds, held.lease_id = False, None

    def release(self, agent: str, reason: str) -> None:
        held = self._held_for(agent)
        try:
            self._call(agent, "POST", "/api/claims/release", {"reason": reason})
        except ApiError as err:
            if err.status != 409:
                raise
            # The lease was already gone, which is what releasing wanted.
            self._log.warning(
                "%s: release found no lease (ticket #%s already handed over); supervisor's reason: %s (%s)",
                agent, held.ticket_id, reason, err,
            )
        held.holds, held.lease_id = False, None

    def report_slot_state(self, agent: str, snapshot: SlotSnapshot) -> None:
        body: dict[str, object] = {
            "phase": snapshot.phase,
            "phase_age_seconds": max(0.0, snapshot.phase_age_seconds),
            "error_streak": snapshot.error_streak,
        }
        if snapshot.ticket_id is not None:
            body["ticket_id"] = snapshot.ticket_id
        if snapshot.error_class:
            body["error_class"] = snapshot.error_class
        if snapshot.pause_reason:
            body["pause_reason"] = snapshot.pause_reason
        if snapshot.reason:
            body["reason"] = snapshot.reason
            if snapshot.reason_detail:
                body["reason_detail"] = snapshot.reason_detail
        if snapshot.layout is not None:
            body["layout"] = snapshot.layout
        if snapshot.bundle_hash:
            body["bundle_hash"] = snapshot.bundle_hash
        if snapshot.idle:
            body["idle"] = True
        # Bounded like the usage report: a report that waited out an api outage would hold the
        # slot's thread, and the next report replaces this one anyway.
        self._call(agent, "POST", "/api/worker/slot-state", body, timeout=15.0, attempts=2)

    def report_usage(self, agent: str, usage: Usage) -> None:
        """Totals for the session so far: the server replaces its last report with each one."""
        session = self._usage.get(agent)
        if session is None or not usage.models:
            return
        for entry in usage.models:
            session.totals.add_delta(entry.provider, entry.model, entry.billing_mode, entry.counters)
        records = session.totals.records(BILLING_MODE, session.harness)
        if not records:
            return
        body = {
            "transcript_key": session.session_id,
            "harness": session.harness,
            "session_id": session.session_id,
            "scope": "main",
            "source": "supervisor",
            "reporter_revision": USAGE_REVISION,
            "records": records,
        }
        try:
            # Nothing waits on a usage report.
            self._call(agent, "POST", "/hooks/usage", body, timeout=15.0, attempts=2)
        except ApiError as err:
            self._log.warning("%s: usage report not taken: %s", agent, err)
