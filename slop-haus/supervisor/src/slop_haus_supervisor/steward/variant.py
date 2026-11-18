from __future__ import annotations

import logging
from collections.abc import Callable

from slop_haus_supervisor.workspace.harness_config import HarnessConfigError, HarnessConfigStore


class StewardHarnessVariant:
    """The steward agent's effective harness config as a run variant.

    The server lays the steward's own override over the tenant's defaults when the steward's own
    credential asks for its config, exactly as for a worker, so there is no resolution here. A server
    that cannot be reached leaves the copy saved last time, which `refresh` uses itself.
    """

    def __init__(self, store: HarnessConfigStore, logger: logging.Logger, harness: Callable[[], str] = lambda: "claude") -> None:
        self._store = store
        self._harness = harness
        self._log = logger

    def variant(self) -> str:
        try:
            config = self._store.refresh()
            return config.variant(self._harness()) if config is not None else ""
        except HarnessConfigError as err:
            # A run on the harness's own default beats no run: the steward's briefing must not wait on a config file.
            self._log.warning("steward: running without a harness config, could not read it: %s", err)
            return ""
