"""The one place a harness is chosen from `config.harness`: slots and the steward come through here.

Remote Control keeps its `RemoteControl*` subclasses, which differ in where their state lives, not in what is chosen.
"""

from __future__ import annotations

from collections.abc import Mapping, Sequence
from dataclasses import dataclass, field
from enum import StrEnum
from pathlib import Path

from slop_haus_supervisor.harness.base import AgentEnv, ConfiguredHarness
from slop_haus_supervisor.harness.claude import ClaudeHarness
from slop_haus_supervisor.harness.codex import CodexHarness
from slop_haus_supervisor.harness.codex_login import CodexLogin
from slop_haus_supervisor.harness.denied import DENIED_TOOLS
from slop_haus_supervisor.harness import registry
from slop_haus_supervisor.harness.pi import PiHarness
from slop_haus_supervisor.harness.text import SecretMasker
from slop_haus_supervisor.harness.variant import InvalidVariant, parse_variant

HARNESSES = registry.NAMES


class HarnessKind(StrEnum):
    SLOT = "slot"
    STEWARD = "steward"


class UnsupportedHarness(Exception):
    pass


@dataclass(frozen=True)
class ToolPolicy:
    """What a run may use; the steward's is an allowlist, a slot's only the shared deny-list."""

    command: Sequence[str] = ("claude",)
    denied: Sequence[str] = DENIED_TOOLS
    allowed: Sequence[str] = ()
    builtin: Sequence[str] = ()


@dataclass(frozen=True)
class HarnessContext:
    root: Path
    server: str
    credentials: Mapping[str, str] = field(default_factory=lambda: dict[str, str]())
    masker: SecretMasker | None = None
    # The steward credential's helper; slots find their own beside the workspace.
    token_script: Path | None = None
    # pi only: slop-haus's tools reach it through this extension.
    pi_extension: Path | None = None
    policy: ToolPolicy = field(default_factory=ToolPolicy)
    # Per-agent layers over `credentials`; slots only.
    agent_env: AgentEnv | None = None
    # codex only: set when several slots share one ChatGPT login.
    codex_login: CodexLogin | None = None


def build_harness(kind: HarnessKind, harness: str, context: HarnessContext, *, default_variant: str = "") -> ConfiguredHarness:
    """`default_variant` is parsed here so a bad one fails at start-up, not at the first claim."""
    try:
        parse_variant(default_variant)
    except InvalidVariant as err:
        raise UnsupportedHarness(str(err)) from err
    entry = registry.find(harness)
    if entry is None:
        raise UnsupportedHarness(f"unknown harness {harness!r}: use one of {', '.join(HARNESSES)}")
    # "conditional" passes here: that harness's steward is built, and its host checked, by build_steward_harness.
    if kind is HarnessKind.STEWARD and entry.steward == "no":
        # pi runs every tool call without asking, so the steward's read-only allowlist cannot be held.
        raise UnsupportedHarness(f"the {kind} cannot run on {entry.name}: it has no read-only tool allowlist")
    if kind is HarnessKind.STEWARD and harness == "codex":
        # The Codex steward's confinement and probe live in steward/harness.py; a plain CodexHarness here would run unconfined.
        raise UnsupportedHarness("the codex steward is built by build_steward_harness, which holds its sandbox policy")
    if kind is HarnessKind.SLOT and not entry.worker:
        raise UnsupportedHarness(f"slots cannot run on {entry.name}")
    if harness == "claude":
        script = context.token_script
        return ClaudeHarness(
            context.root,
            context.server,
            "",
            command=context.policy.command,
            denied_tools=context.policy.denied,
            allowed_tools=context.policy.allowed,
            builtin_tools=context.policy.builtin,
            credentials=context.credentials,
            agent_env=context.agent_env,
            masker=context.masker,
            token_script=(lambda _spec: script) if script is not None else None,
        )
    if harness == "pi":
        if context.pi_extension is None:
            raise UnsupportedHarness("pi needs slop-haus's extension (<root>/.slop-haus/pi/slop-haus.ts)")
        return PiHarness(context.root, context.pi_extension, "", credentials=context.credentials, agent_env=context.agent_env)
    if harness == "codex":
        return CodexHarness(context.root, context.server, credentials=context.credentials, agent_env=context.agent_env, login=context.codex_login)
    raise UnsupportedHarness(f"unknown harness {harness!r}: use one of {', '.join(HARNESSES)}")
