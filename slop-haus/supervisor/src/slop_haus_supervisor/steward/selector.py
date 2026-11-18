from __future__ import annotations

import logging
from collections.abc import Callable, Sequence
from pathlib import Path

from slop_haus_supervisor.harness.base import Harness
from slop_haus_supervisor.harness.factory import UnsupportedHarness
from slop_haus_supervisor.harness.text import SecretMasker
from slop_haus_supervisor.steward.codex_probe import SandboxUnsafe
from slop_haus_supervisor.steward.ports import StewardStatus
from slop_haus_supervisor.workspace.harness_config import HarnessConfigError, HarnessConfigStore

DEFAULT_HARNESS = "claude"
# The server stores at most this much of a reason.
MAX_REASON = 500


class StewardHarnessSelector:
    """The steward's harness for each run, from the harness the owner set for the steward agent on the board.

    A changed choice rebuilds at the next run. A Codex steward is probed whenever it is built, and a refusal leaves no
    steward running -- never a Claude one in its place, which would mix models in the agreement record.
    """

    def __init__(
        self,
        store: HarnessConfigStore,
        build: Callable[[str, Sequence[Path]], Harness],
        review_roots: Callable[[], Sequence[Path]],
        status: StewardStatus,
        logger: logging.Logger,
        masker: SecretMasker | None = None,
    ) -> None:
        self._store = store
        self._build = build
        self._review_roots = review_roots
        self._status = status
        self._log = logger
        self._masker = masker or SecretMasker()
        self._name = DEFAULT_HARNESS
        self._built: tuple[tuple[str, tuple[Path, ...]], Harness] | None = None

    @property
    def name(self) -> str:
        """The harness `current` last chose."""
        return self._name

    def _chosen(self) -> str:
        try:
            config = self._store.refresh()
        except HarnessConfigError as err:
            # The harness already built beats a guess: an unreachable server must not flip the steward's model.
            self._log.warning("steward: could not read the harness config, keeping %s: %s", self._name, self._masker.mask(str(err)))
            return self._name
        return (config.harness if config is not None else "") or DEFAULT_HARNESS

    def current(self) -> Harness:
        name = self._chosen()
        self._name = name
        # Only a Codex steward's sandbox holds the roots; a slot added later needs a rebuild (and a fresh probe) to be readable.
        roots = tuple(self._review_roots()) if name == "codex" else ()
        if self._built is not None and self._built[0] == (name, roots):
            return self._built[1]
        self._built = None
        try:
            harness = self._build(name, roots)
        except (SandboxUnsafe, UnsupportedHarness) as err:
            detail = self._masker.mask(str(err))
            self._log.warning("steward: not started on %s: %s", name, detail)
            self._status.report("failed", self._reason(name, err, detail))
            raise
        self._built = ((name, roots), harness)
        # Once per build, so a refusal reported by an earlier build (or an earlier supervisor) is cleared.
        self._status.report("running")
        return harness

    @staticmethod
    def _reason(name: str, err: Exception, detail: str) -> str:
        what = "sandbox probe refused" if isinstance(err, SandboxUnsafe) else "cannot run the steward"
        return f"{name.capitalize()} steward not started: {what}: {detail}"[:MAX_REASON]
