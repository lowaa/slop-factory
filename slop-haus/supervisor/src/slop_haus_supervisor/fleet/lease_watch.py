"""What a slot watches on the server while it holds a ticket: the lease, the lock, and messages."""

from __future__ import annotations

from slop_haus_supervisor.fleet import policy
from slop_haus_supervisor.fleet.policy import PolicyConfig
from slop_haus_supervisor.ports import Alerts, Clock
from slop_haus_supervisor.slophaus.client import EventKind, LockState, SlopHaus, WorkerState


class LeaseWatch:
    def __init__(self, agent: str, client: SlopHaus, alerts: Alerts, config: PolicyConfig, clock: Clock) -> None:
        self._agent = agent
        self._client = client
        self._alerts = alerts
        self._config = config
        self._clock = clock
        self.last_heartbeat = 0.0
        self.lock_held_since: float | None = None
        self._lock_alerted = False
        # Events are delivered once, so whatever a look at them does not act on is kept.
        self.message_pending = False

    def start(self) -> None:
        """A newly held ticket: nothing is known about its lock or messages yet."""
        self.message_pending = False
        self.lock_clear()
        self.last_heartbeat = self._clock.now()

    def lock_clear(self) -> None:
        self.lock_held_since = None
        self._lock_alerted = False

    def lock_taken(self) -> None:
        self.lock_held_since = self._clock.now()

    def heartbeat_due_in(self) -> float:
        return self.last_heartbeat + self._config.heartbeat_interval

    def maintain(self) -> bool:
        """Heartbeat for as long as the ticket is held, in every phase.

        The server drops a lock-queue place when the lease lapses, and a queued agent
        does not keep its lease alive, so waiting states need this as much as runs do.
        Returns False when the lease turned out to be gone.
        """
        now = self._clock.now()
        if now - self.last_heartbeat < self._config.heartbeat_interval:
            return True
        state = self.observe()
        if not state.lease_held:
            return False
        self._client.heartbeat(self._agent)
        self.last_heartbeat = now
        return True

    def observe(self) -> WorkerState:
        """Read the server's view and track how long the lock has been held."""
        state = self._client.state(self._agent)
        now = self._clock.now()
        if state.lock == LockState.HELD:
            if self.lock_held_since is None:
                self.lock_held_since = now
        else:
            self.lock_clear()
        if policy.lock_alert_due(self._config, now, self.lock_held_since, self._lock_alerted):
            self._lock_alerted = True
            held_for = int((now - (self.lock_held_since or now)) / 60)
            self._alerts.alert(f"{self._agent} has held the integration lock for {held_for} minutes: slow queue?")
        return state

    def revoked(self) -> bool:
        """Poll without blocking; a revocation is returned, a person's message is kept."""
        revoked = False
        for event in self._client.poll(self._agent, 0):
            if event.kind == EventKind.LEASE_REVOKED:
                revoked = True
            elif event.kind == EventKind.MESSAGE:
                self.message_pending = True
        return revoked

    def take_message(self) -> bool:
        pending = self.message_pending
        self.message_pending = False
        return pending
