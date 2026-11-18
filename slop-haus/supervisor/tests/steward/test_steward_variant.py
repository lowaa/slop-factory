from __future__ import annotations

import json
import logging

import pytest

from slop_haus_supervisor.steward.variant import StewardHarnessVariant
from slop_haus_supervisor.workspace.harness_config import HarnessConfig, HarnessConfigError


class FakeStore:
    def __init__(self, result: HarnessConfig | None | Exception) -> None:
        self.result = result

    def refresh(self) -> HarnessConfig | None:
        if isinstance(self.result, Exception):
            raise self.result
        return self.result


def config(settings: dict[str, dict[str, str]], harness: str = "") -> HarnessConfig:
    parsed = HarnessConfig.parse(json.dumps({"revision": 3, "harness": harness, "settings": settings}).encode())
    assert parsed is not None
    return parsed


def variant_of(result: HarnessConfig | None | Exception) -> str:
    return StewardHarnessVariant(FakeStore(result), logging.getLogger("t")).variant()  # pyright: ignore[reportArgumentType]


def test_the_effective_model_and_effort_become_the_variant() -> None:
    assert variant_of(config({"claude": {"model": "claude-opus-4-7", "effort": "high"}})) == "model=claude-opus-4-7,effort=high"


def test_the_subagent_model_is_carried_too() -> None:
    assert variant_of(config({"claude": {"model": "m1", "subagent_model": "m2"}})) == "model=m1,subagent_model=m2"


def test_a_setting_the_config_leaves_out_stays_out() -> None:
    assert variant_of(config({"claude": {"effort": "low"}})) == "effort=low"


def test_no_config_is_the_harness_default() -> None:
    assert variant_of(None) == ""


def test_pi_settings_do_not_reach_a_claude_run() -> None:
    assert variant_of(config({"pi": {"model": "x"}}, "pi")) == ""


def test_values_that_fail_their_pattern_are_left_out() -> None:
    assert variant_of(config({"claude": {"model": "--bad flag", "effort": "turbo"}})) == ""


def test_an_unreadable_config_runs_on_the_default_and_says_why(caplog: pytest.LogCaptureFixture) -> None:
    with caplog.at_level(logging.WARNING, logger="t"):
        assert variant_of(HarnessConfigError("x is a symlink")) == ""
    assert "x is a symlink" in caplog.text


def test_the_variant_reads_the_settings_of_the_harness_the_steward_runs_on() -> None:
    settings = {"claude": {"model": "m1"}, "codex": {"model": "m2"}}
    assert StewardHarnessVariant(FakeStore(config(settings)), logging.getLogger("t"), lambda: "codex").variant() == "model=m2"  # pyright: ignore[reportArgumentType]
    assert StewardHarnessVariant(FakeStore(config(settings)), logging.getLogger("t"), lambda: "claude").variant() == "model=m1"  # pyright: ignore[reportArgumentType]


def test_a_setting_the_harness_refuses_runs_the_steward_on_the_default(caplog: pytest.LogCaptureFixture) -> None:
    bad = config({"codex": {"effort": "extreme"}})
    with caplog.at_level(logging.WARNING, logger="t"):
        assert StewardHarnessVariant(FakeStore(bad), logging.getLogger("t"), lambda: "codex").variant() == ""  # pyright: ignore[reportArgumentType]
    assert "codex effort" in caplog.text
