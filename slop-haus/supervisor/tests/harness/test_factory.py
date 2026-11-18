from __future__ import annotations

from pathlib import Path

import pytest

from slop_haus_supervisor.harness.claude import ClaudeHarness
from slop_haus_supervisor.harness.codex import CodexHarness
from slop_haus_supervisor.harness.denied import DENIED_TOOLS
from slop_haus_supervisor.harness.factory import HarnessContext, HarnessKind, UnsupportedHarness, build_harness
from slop_haus_supervisor.harness.pi import SLOP_HAUS_TOOLS, PiHarness
from slop_haus_supervisor.steward.guard import steward_denied_tools

PREFIX = "mcp__slop-haus__"


def context(tmp_path: Path, **kw: object) -> HarnessContext:
    return HarnessContext(tmp_path, "https://s", **kw)  # pyright: ignore[reportArgumentType]


def test_claude_is_chosen_by_name(tmp_path: Path) -> None:
    assert isinstance(build_harness(HarnessKind.SLOT, "claude", context(tmp_path)), ClaudeHarness)


def test_pi_is_chosen_by_name(tmp_path: Path) -> None:
    ext = tmp_path / "e.ts"
    assert isinstance(build_harness(HarnessKind.SLOT, "pi", context(tmp_path, pi_extension=ext)), PiHarness)


def test_the_steward_cannot_run_on_pi(tmp_path: Path) -> None:
    with pytest.raises(UnsupportedHarness, match="steward"):
        build_harness(HarnessKind.STEWARD, "pi", context(tmp_path, pi_extension=tmp_path / "e.ts"))


def test_an_unknown_harness_and_a_bad_variant_are_refused(tmp_path: Path) -> None:
    with pytest.raises(UnsupportedHarness, match="unknown"):
        build_harness(HarnessKind.SLOT, "gpt", context(tmp_path))
    with pytest.raises(UnsupportedHarness, match="variant"):
        build_harness(HarnessKind.SLOT, "claude", context(tmp_path), default_variant="bogus=1")


def test_claude_needs_a_login_key(tmp_path: Path) -> None:
    assert "ANTHROPIC_API_KEY" in build_harness(HarnessKind.SLOT, "claude", context(tmp_path)).missing_credentials()
    keyed = context(tmp_path, credentials={"ANTHROPIC_API_KEY": "k"})
    assert build_harness(HarnessKind.SLOT, "claude", keyed).missing_credentials() == ""


def test_pi_needs_a_provider_key_or_its_login(tmp_path: Path) -> None:
    ext = tmp_path / "e.ts"
    assert build_harness(HarnessKind.SLOT, "pi", context(tmp_path, pi_extension=ext)).missing_credentials()
    keyed = context(tmp_path, pi_extension=ext, credentials={"OPENAI_API_KEY": "k"})
    assert build_harness(HarnessKind.SLOT, "pi", keyed).missing_credentials() == ""


def test_pi_login_counts_but_an_unrelated_secret_does_not(tmp_path: Path) -> None:
    ext = tmp_path / "e.ts"
    other = context(tmp_path, pi_extension=ext, credentials={"GITHUB_TOKEN": "t"})
    assert build_harness(HarnessKind.SLOT, "pi", other).missing_credentials()
    (tmp_path / "harness" / "pi").mkdir(parents=True)
    (tmp_path / "harness" / "pi" / "auth.json").write_text("{}")
    assert build_harness(HarnessKind.SLOT, "pi", other).missing_credentials() == ""


def test_pi_offers_none_of_the_denied_tools() -> None:
    denied = {name.removeprefix(PREFIX) for name in DENIED_TOOLS}
    assert not denied & set(SLOP_HAUS_TOOLS)


def test_the_steward_denies_the_shared_list() -> None:
    assert set(DENIED_TOOLS) <= set(steward_denied_tools())


def test_codex_is_chosen_for_slots_only(tmp_path: Path) -> None:
    assert isinstance(build_harness(HarnessKind.SLOT, "codex", context(tmp_path)), CodexHarness)
    # Its steward is "conditional", which only build_steward_harness may grant.
    with pytest.raises(UnsupportedHarness, match="steward"):
        build_harness(HarnessKind.STEWARD, "codex", context(tmp_path))
