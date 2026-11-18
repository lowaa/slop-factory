from __future__ import annotations

from collections.abc import Callable, Mapping

from slop_haus_supervisor.fleet.scheduler import Drainable, Scheduler, Steppable


class SchedulerHost:
    """The reconciler's view of the running fleet: the scheduler, and how to build a slot it has not got yet."""

    def __init__(
        self, scheduler: Scheduler, make_slot: Callable[[str, str], Steppable],
        make_host: Callable[[str, str], Steppable],
        harness_of: Callable[[str], str],
    ) -> None:
        self._scheduler = scheduler
        self._make_slot = make_slot
        self._make_host = make_host
        self._harness_of = harness_of

    def slot_names(self) -> set[str]:
        return self._scheduler.slot_names()

    def start_slot(self, name: str, harness: str) -> None:
        self._scheduler.add_slot(self._make_slot(name, harness))

    def harness_of(self, name: str) -> str:
        return self._harness_of(name)

    def start_host(self, name: str, harness: str, held: bool) -> None:
        host = self._make_host(name, harness)
        # Before the scheduler steps it, so an owner's hold never starts a session first.
        if isinstance(host, Drainable):
            host.set_held(held)
        self._scheduler.add_slot(host)

    def hold(self, name: str, held: bool) -> None:
        self._scheduler.hold(name, held)

    def pause_reason(self, name: str) -> str | None:
        return self._scheduler.pause_reason(name)

    def unpause(self, names: list[str] | None) -> Mapping[str, str]:
        return self._scheduler.unpause(names)

    def retire(self, name: str) -> bool:
        return self._scheduler.retire(name)
