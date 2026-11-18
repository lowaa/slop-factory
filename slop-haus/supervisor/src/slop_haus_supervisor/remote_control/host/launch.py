from __future__ import annotations

import logging
from collections.abc import Callable, MutableMapping
from dataclasses import dataclass, field
from pathlib import Path

from slop_haus_supervisor.ports import ProcessRunner
from slop_haus_supervisor.service.run_env import LauncherEnv, LauncherEnvError
from slop_haus_supervisor.workspace.harness_config import HarnessConfig, HarnessConfigError
from slop_haus_supervisor.workspace.provisioned import Kind, Provisioned

_PREFIX = "SLOP_HAUS_"


class LaunchRefused(Exception):
    pass


@dataclass(frozen=True)
class Applied:
    """What a config sets that is not environment: pi's flags, by setting name, and the variant text Codex is given."""

    flags: dict[str, str] = field(default_factory=lambda: {})
    variant: str = ""


class PythonLaunch:
    """What `run_remote_control.sh` does between the update and starting the host, done in this process.

    The order is the launcher's, and it matters: nothing a repo controls (run.env, and the files it
    includes) is read until the stray `SLOP_HAUS_*` variables are gone and the credential-holding steps
    (the harness config fetch, which runs token.sh) are over, so token.sh never runs with a repo's
    `.env` in its environment.
    """

    def __init__(self, root: Path, environ: MutableMapping[str, str], runner: ProcessRunner, logger: logging.Logger) -> None:
        self._root = root
        self._environ = environ
        self._log = logger
        self._unit = Provisioned(root, Kind.REMOTE_CONTROL, runner, logger)
        # What the config set, and what each variable was before it did (None: unset), so a later config
        # that drops a setting gives back run.env's value, or nothing, rather than keep the old one.
        self._underneath: dict[str, str | None] = {}
        self._revision: int | None = None

    def clear_stray_environment(self) -> None:
        """A human's leftover SLOP_HAUS_TOKEN, or a SLOP_HAUS_URL for another environment, would follow the host into every hook."""
        for name in [n for n in self._environ if n.startswith(_PREFIX)]:
            del self._environ[name]

    def run(self, harness: str) -> Applied:
        """Prepares the environment for `harness` and returns what its config sets besides that (pi's flags, Codex's variant)."""
        self.clear_stray_environment()
        store = self._unit.harness_config()
        config = store.refresh()
        self._refuse_other_harness(config, harness)
        self._environ.update(LauncherEnv(self._root, self._log.warning).load(self._root / ".slop-haus" / "run.env"))
        # After run.env, which may not set it: which harness this is is the host's to say.
        self._environ[_PREFIX + "HARNESS"] = harness
        return self._apply(config, harness)

    def reload(self, harness: str) -> Applied | None:
        """The config fetched again and applied, if it changed since it was applied: then what it sets
        besides environment, as `run` returns it; None when it is the same revision. A config that moved the agent to
        another harness is not applied: this process cannot become another harness, and says so."""
        config = self._unit.harness_config().refresh()
        if (config.revision if config is not None else None) == self._revision:
            return None
        if config is not None and config.harness and config.harness != harness:
            self._log.warning("the harness config now runs %s, not %s: restart this remote control to take it up", config.harness, harness)
            return None
        return self._apply(config, harness)

    def _apply(self, config: HarnessConfig | None, harness: str) -> Applied:
        # First, so a Codex value it would refuse (HarnessConfigError, as on the worker path) leaves the
        # environment and the applied revision as they were: the harness keeps the config it has.
        applied = Applied(config.flags(harness), config.variant(harness) if harness == "codex" else "") if config is not None else Applied()
        settings = config.env(harness) if config is not None else {}
        for name in set(self._underneath) - set(settings):
            self._put_back(name)
        for name, value in settings.items():
            self._underneath.setdefault(name, self._environ.get(name))
            # What the config sets beats run.env; what it leaves unset, run.env still decides.
            self._environ[name] = value
        self._revision = config.revision if config is not None else None
        if config is None:
            self._environ.pop(_PREFIX + "CONFIG_REVISION", None)
            return applied
        self._environ[_PREFIX + "CONFIG_REVISION"] = str(config.revision)
        return applied

    def warn(self, message: str) -> None:
        self._log.warning("%s", message)

    def _put_back(self, name: str) -> None:
        before = self._underneath.pop(name)
        if before is None:
            self._environ.pop(name, None)
        else:
            self._environ[name] = before

    @staticmethod
    def _refuse_other_harness(config: HarnessConfig | None, started: str) -> None:
        """An agent in a variant is the variant's harness and no other: slop-haus gives it no work from any other."""
        if config is not None and config.harness and config.harness != started:
            raise LaunchRefused(
                f"this agent is in a variant that runs {config.harness}, not {started} -- start it with "
                f".slop-haus/run_remote_control.sh --harness {config.harness}, or ask an owner to take it out of the variant"
            )


class ConfigWatch:
    """The host's view of its harness config between turns: whether it changed since the harness was
    started with it. `on_applied` gives the new flags (pi) or variant (Codex) to the harness object; Claude Code's settings are
    environment variables, which `PythonLaunch` has put in place for the next process by then."""

    def __init__(self, launch: PythonLaunch, harness: str, on_applied: Callable[[Applied], None]) -> None:
        self._launch = launch
        self._harness = harness
        self._on_applied = on_applied

    def changed(self) -> bool:
        try:
            applied = self._launch.reload(self._harness)
        except (OSError, LauncherEnvError, HarnessConfigError) as e:
            # The harness keeps the config it has: a turn is not held up by a config it cannot read.
            self._launch.warn(f"the harness config could not be read again, so the session keeps the one it has: {e}")
            return False
        if applied is None:
            return False
        self._on_applied(applied)
        return True
