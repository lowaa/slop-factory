from __future__ import annotations

from collections.abc import Mapping, Sequence
from pathlib import Path
from typing import Literal, overload

from slop_haus_supervisor.harness import registry
from slop_haus_supervisor.harness.base import ConfiguredHarness
from slop_haus_supervisor.harness.claude import ClaudeHarness
from slop_haus_supervisor.harness.codex_login import CodexLogin
from slop_haus_supervisor.harness.factory import HarnessContext, HarnessKind, ToolPolicy, UnsupportedHarness, build_harness
from slop_haus_supervisor.harness.text import SecretMasker
from slop_haus_supervisor.steward.codex_harness import CodexStewardHarness
from slop_haus_supervisor.steward.codex_policy import CodexStewardPolicy
from slop_haus_supervisor.steward.codex_probe import CodexAppServerExec, SandboxProbe
from slop_haus_supervisor.steward.guard import STEWARD_ALLOWED_TOOLS, STEWARD_BUILTIN_TOOLS, scratch_dir, steward_denied_tools
from slop_haus_supervisor.steward.runner import StewardConfig, steward_command


@overload
def build_steward_harness(
    root: Path, server: str, config: StewardConfig, credentials: Mapping[str, str], masker: SecretMasker, token_script: Path,
    harness: Literal["claude"] = "claude", review_roots: Sequence[Path] = (), forbidden: Sequence[Path] = (),
    codex_login: CodexLogin | None = None,
) -> ClaudeHarness: ...
@overload
def build_steward_harness(
    root: Path, server: str, config: StewardConfig, credentials: Mapping[str, str], masker: SecretMasker, token_script: Path,
    harness: str, review_roots: Sequence[Path] = (), forbidden: Sequence[Path] = (),
    codex_login: CodexLogin | None = None,
) -> ConfiguredHarness: ...
def build_steward_harness(
    root: Path, server: str, config: StewardConfig, credentials: Mapping[str, str], masker: SecretMasker, token_script: Path,
    harness: str = "claude", review_roots: Sequence[Path] = (), forbidden: Sequence[Path] = (),
    codex_login: CodexLogin | None = None,
) -> ConfiguredHarness:
    """A harness for steward runs only: the tool allowlist, deny-list and turn budget are fixed here, so no
    caller can start a steward without them. `token_script` is the steward credential's helper (B3).

    A Codex steward is probed first and not built (SandboxUnsafe) if its sandbox does not hold: `review_roots` are the
    only checkouts its shell may read, `forbidden` the directories a canary is placed in to prove it cannot read them.
    """
    entry = registry.find(harness)
    if entry is None or entry.steward == "no":
        raise UnsupportedHarness(f"the steward cannot run on {harness}: it has no read-only tool allowlist")
    if harness == "codex":
        policy = CodexStewardPolicy.for_command(("codex",), scratch_dir(root), review_roots)
        built = CodexStewardHarness(root, server, policy, config.max_turns, credentials=credentials, login=codex_login)
        SandboxProbe(CodexAppServerExec(("codex",), policy, built.env()), policy, forbidden).check()
        return built
    tools = ToolPolicy(
        command=steward_command(config.max_turns),
        denied=steward_denied_tools(),
        allowed=STEWARD_ALLOWED_TOOLS,
        builtin=STEWARD_BUILTIN_TOOLS,
    )
    built = build_harness(
        HarnessKind.STEWARD, harness, HarnessContext(root, server, credentials, masker, token_script, policy=tools)
    )
    # The read-only allowlist is Claude Code's; the factory refuses any other harness for a steward.
    assert isinstance(built, ClaudeHarness)
    return built
