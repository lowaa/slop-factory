"""The host's env files in layers, and the one place that knows them.

Three layers, each replacing the one before key by key: base (`<root>/<file>.env`, every agent), kind
(`<root>/remote-control/<file>.env`, Remote Control slots only) and agent (`<root>/agents/<name>/<file>.env`,
whatever the agent's kind). `<file>` is secrets, run or repo. Everything here stays on the host.
"""

from __future__ import annotations

import os
import re
from collections.abc import Callable
from dataclasses import dataclass
from enum import StrEnum
from importlib import metadata
from pathlib import Path

from slop_haus_supervisor.service.run_env import OWN_SETTINGS, controls_execution
from slop_haus_supervisor.workspace import config as cfg
from slop_haus_supervisor.workspace import envfile
from slop_haus_supervisor.workspace.envfile import EnvFile, EnvFileError
from slop_haus_supervisor.workspace.layout import Layout

# Settings the supervisor itself reads from run.env; run.env only, at every layer.
OPERATOR_SETTINGS = OWN_SETTINGS
# A mask line withholds a base key from the kind layer's agents: `!NAME`.
_MASK = re.compile(r"!([A-Za-z_][A-Za-z0-9_]*)")


class AgentKind(StrEnum):
    WORKER = "worker"
    REMOTE_CONTROL = "remote-control"


class EnvKind(StrEnum):
    SECRETS = "secrets"
    RUN = "run"
    REPO = "repo"

    @property
    def filename(self) -> str:
        return f"{self.value}.env"


class Layer(StrEnum):
    BASE = "base"
    KIND = "remote-control"
    AGENT = "agent"


@dataclass(frozen=True)
class Resolved:
    value: str
    layer: Layer


@dataclass(frozen=True)
class LayerRef:
    """One layer to read or edit; `agent` names the directory for Layer.AGENT only."""

    layer: Layer
    agent: str = ""

    def directory(self, layout: Layout) -> Path:
        match self.layer:
            case Layer.BASE:
                return layout.root
            case Layer.KIND:
                return layout.remote_control_dir
            case Layer.AGENT:
                return layout.agent_dir(self.agent)

    def path(self, layout: Layout, file: EnvKind) -> Path:
        return self.directory(layout) / file.filename


@dataclass(frozen=True)
class LayerContent:
    values: dict[str, str]
    masked: frozenset[str]
    # Unknown SLOP_HAUS_ names left out of `values`; only ever the base run.env's (see `is_dropped`).
    dropped: tuple[str, ...] = ()


def is_dropped(file: EnvKind, layer: Layer, name: str) -> bool:
    """Whether the base run.env's `name` is a SLOP_HAUS_ setting this supervisor does not know, which is ignored rather than refused.

    A setting written before an `update` reaches an older supervisor must not stop every run (owner's decision).
    The other layers keep refusing. That dropping is safe rests on every operator setting being safe when unset (contracts/env.json).
    """
    return file is EnvKind.RUN and layer is Layer.BASE and name.startswith("SLOP_HAUS_") and name not in OPERATOR_SETTINGS


def dropped_message(name: str) -> str:
    try:
        version = metadata.version("slop-haus-supervisor")
    except metadata.PackageNotFoundError:
        version = "unknown"
    return f"run.env: {name} isn't known to this supervisor (version {version}), ignored; run `slop-haus update`"


def refusal(file: EnvKind, layer: Layer, name: str) -> str | None:
    """Why `name` may not be set in `file` at `layer`, or None. The reason never depends on the layer except for PATH."""
    if name.startswith("SLOP_HAUS_"):
        if file is EnvKind.RUN and name in OPERATOR_SETTINGS:
            return None
        return f"refusing {name} -- SLOP_HAUS_ names are slop-haus's own and are set by its files, not here"
    if name == "PATH":
        if file is EnvKind.RUN and layer is Layer.BASE:
            return None
        return "refusing PATH -- it is set in the base layer's run.env only"
    if controls_execution(name):
        return f"refusing {name} -- it changes what every shell or program the agent starts runs"
    return None


def _inside_root(path: Path, root: Path) -> bool:
    try:
        return path.resolve(strict=True).is_relative_to(root.resolve(strict=True))
    except OSError:
        return False


def read_layer(layout: Layout, ref: LayerRef, file: EnvKind) -> LayerContent:
    """A layer's file, validated: a missing file is empty and a forbidden name is refused with its file and line, never its value."""
    path = ref.path(layout, file)
    if not os.path.lexists(path):
        return LayerContent({}, frozenset())
    # An owner's base files may be links to a vault; the layers added here stay inside the root.
    if ref.layer is not Layer.BASE and not _inside_root(path, layout.root):
        raise EnvFileError(f"{path} leads outside {layout.root} -- refusing to read it")
    values: dict[str, str] = {}
    masked: set[str] = set()
    dropped: list[str] = []
    for number, line in enumerate(path.read_text().splitlines(), start=1):
        mask = _MASK.fullmatch(line.strip())
        if mask:
            if ref.layer is not Layer.KIND:
                raise EnvFileError(f"{path}:{number}: a mask (!NAME) belongs in the {Layer.KIND} layer only")
            masked.add(mask[1])
            continue
        parsed = envfile.parse_line(line)
        if parsed is None:
            continue
        name, value = parsed
        if is_dropped(file, ref.layer, name):
            if name not in dropped:
                dropped.append(name)
            continue
        why = refusal(file, ref.layer, name)
        if why:
            raise EnvFileError(f"{path}:{number}: {why}")
        values[name] = value
    # A blank secret is a placeholder, not a credential: it must not blank what a lower layer holds.
    if file is EnvKind.SECRETS:
        values = {k: v for k, v in values.items() if v}
    return LayerContent({k: v for k, v in values.items() if k not in masked}, frozenset(masked), tuple(dropped))


class LayeredEnv:
    """What an agent of `kind` named `agent_name` gets from the host's env files.

    Workers never see the kind layer: that filter is the one `if` in `_layers`.
    """

    def __init__(
        self, layout: Layout, kind: AgentKind, agent_name: str | None = None, warn: Callable[[str], None] = lambda _message: None
    ) -> None:
        self._warn = warn
        self._layout = layout
        self._kind = kind
        self._agent = cfg.check_name("agent", agent_name) if agent_name else None

    def _layers(self) -> list[LayerRef]:
        layers = [LayerRef(Layer.BASE)]
        if self._kind is AgentKind.REMOTE_CONTROL:
            layers.append(LayerRef(Layer.KIND))
        if self._agent:
            layers.append(LayerRef(Layer.AGENT, self._agent))
        return layers

    def resolve(self, file: EnvKind) -> dict[str, Resolved]:
        merged: dict[str, Resolved] = {}
        for ref in self._layers():
            content = read_layer(self._layout, ref, file)
            for name in content.dropped:
                self._warn(dropped_message(name))
            for name in content.masked:
                merged.pop(name, None)
            merged.update({name: Resolved(value, ref.layer) for name, value in content.values.items()})
        return merged

    def values(self, file: EnvKind) -> dict[str, str]:
        return {name: resolved.value for name, resolved in self.resolve(file).items()}

    def process_env(self) -> dict[str, str]:
        """What a run of this agent has in its environment on top of the host's: secrets, then run.env.

        PATH is left out: it is the host's, prepended once at start-up from the base run.env.
        """
        merged = {**self.values(EnvKind.SECRETS), **self.values(EnvKind.RUN)}
        merged.pop("PATH", None)
        return merged

    def mask_values(self) -> set[str]:
        """What this agent's logs must not show: its effective secrets and repo.env values."""
        return {v for file in (EnvKind.SECRETS, EnvKind.REPO) for v in self.values(file).values()}

    def repo_env_text(self) -> str | None:
        """The `.env` for this agent's repo roots, or None when no repo.env applies.

        The base file alone is copied as it is, comments and all (today's behaviour); once another layer
        contributes, the merged values are written.
        """
        self.resolve(EnvKind.REPO)
        refs = [ref for ref in self._layers() if os.path.lexists(ref.path(self._layout, EnvKind.REPO))]
        if not refs:
            return None
        if refs == [LayerRef(Layer.BASE)]:
            return self._layout.repo_env_file.read_text()
        return "".join(envfile.format_assignment(k, v) + "\n" for k, v in self.values(EnvKind.REPO).items())


def dropped_names(layout: Layout) -> list[str]:
    """The unknown SLOP_HAUS_ names the base run.env holds, for `status`; a file that cannot be read has none to report."""
    try:
        return list(read_layer(layout, LayerRef(Layer.BASE), EnvKind.RUN).dropped)
    except (OSError, UnicodeDecodeError, EnvFileError):
        return []


def known_values(layout: Layout) -> set[str]:
    """Every value any layer of any agent holds, read leniently: for the log masker, which runs before anything is validated."""
    refs = [LayerRef(Layer.BASE), LayerRef(Layer.KIND)]
    agents = layout.root / "agents"
    if agents.is_dir():
        refs += [LayerRef(Layer.AGENT, d.name) for d in agents.iterdir() if d.is_dir() and cfg.NAME_PATTERN.fullmatch(d.name)]
    found: set[str] = set()
    for ref in refs:
        for file in (EnvKind.SECRETS, EnvKind.REPO):
            try:
                found.update(envfile.read(ref.path(layout, file)).values())
            except (OSError, UnicodeDecodeError):
                continue
    return found


@dataclass(frozen=True)
class Listed:
    name: str
    layer: Layer
    note: str = ""


def listing(layout: Layout, ref: LayerRef, file: EnvKind) -> list[Listed]:
    """Names only, never values. A base or kind layer shows its own file; an agent's is the worker's effective set."""
    if ref.layer is Layer.AGENT:
        env = LayeredEnv(layout, AgentKind.WORKER, ref.agent)
        return [Listed(name, r.layer) for name, r in env.resolve(file).items()]
    own = read_layer(layout, ref, file)
    if ref.layer is Layer.BASE:
        raw = envfile.read(ref.path(layout, file))
        return [Listed(name, Layer.BASE, "ignored: unknown to this supervisor" if is_dropped(file, ref.layer, name) else "" if value else "empty") for name, value in raw.items()]
    base = read_layer(layout, LayerRef(Layer.BASE), file)
    out = [Listed(name, Layer.KIND, "overrides base" if name in base.values else "") for name in own.values]
    out += [Listed(name, Layer.KIND, "masks base" if name in base.values else "masks nothing in base") for name in sorted(own.masked)]
    return out


class LayerFile:
    """Edits one layer's file. The name is checked here, as a hand-edited file is on read."""

    def __init__(self, layout: Layout, ref: LayerRef, file: EnvKind) -> None:
        self._ref = ref
        self._file = file
        self._path = ref.path(layout, file)
        self._env = EnvFile(self._path)

    def _check(self, key: str) -> None:
        envfile.check_key(key)
        why = refusal(self._file, self._ref.layer, key)
        if why:
            raise EnvFileError(why)

    def _prepare(self) -> None:
        if self._ref.layer is not Layer.BASE:
            self._path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)

    def set(self, key: str, value: str) -> None:
        self._check(key)
        self._prepare()
        self._env.update(lambda text: envfile.with_value(_without_mask(text, key)[0], key, value))

    def edit_path(self, change: Callable[[str], str]) -> None:
        """Rewrites run.env's text under its lock; PATH is the one name only the base run.env may hold."""
        self._check("PATH")
        self._env.update(change)

    def unset(self, key: str) -> bool:
        envfile.check_key(key)
        if not self._path.exists():
            return False
        return self._env.unset(key)

    def mask(self, key: str) -> None:
        if self._ref.layer is not Layer.KIND:
            raise EnvFileError(f"a mask belongs in the {Layer.KIND} layer only")
        self._check(key)
        self._prepare()
        self._env.update(lambda text: _with_mask(envfile.without(text, key)[0], key))

    def unmask(self, key: str) -> bool:
        if self._ref.layer is not Layer.KIND:
            raise EnvFileError(f"a mask belongs in the {Layer.KIND} layer only")
        envfile.check_key(key)
        if not self._path.exists():
            return False
        removed = False

        def drop(text: str) -> str:
            nonlocal removed
            out, removed = _without_mask(text, key)
            return out

        self._env.update(drop)
        return removed


def _without_mask(text: str, key: str) -> tuple[str, bool]:
    lines = text.splitlines()
    kept = [line for line in lines if line.strip() != f"!{key}"]
    return ("\n".join(kept) + "\n" if kept else ""), len(kept) != len(lines)


def _with_mask(text: str, key: str) -> str:
    return _without_mask(text, key)[0] + f"!{key}\n"
