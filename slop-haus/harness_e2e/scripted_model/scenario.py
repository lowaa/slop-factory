"""A scenario: the ordered turns the scripted model plays back.

A turn is text and/or tool calls, independent of any wire API; each API
adapter in apis/ encodes it. Keeping the format API-free is what lets a
scenario written for one harness run unchanged against another that speaks a
different API.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any


@dataclass(frozen=True)
class ToolCall:
    name: str
    input: dict[str, Any] = field(default_factory=dict)


@dataclass(frozen=True)
class Usage:
    input_tokens: int = 0
    output_tokens: int = 0
    cache_read_input_tokens: int = 0
    cache_creation_input_tokens: int = 0


@dataclass(frozen=True)
class Turn:
    text: str = ""
    tool_calls: tuple[ToolCall, ...] = ()
    usage: Usage = field(default_factory=Usage)
    # Held back until the test opens this gate (PUT /_scripted/gates/<name>):
    # how a test acts on the board between two turns, with a synchronisation
    # point where a sleep would be a race.
    gate: str = ""

    def __post_init__(self) -> None:
        if not self.text and not self.tool_calls:
            raise ValueError("a turn needs text or at least one tool call")


@dataclass(frozen=True)
class Scenario:
    name: str
    turns: tuple[Turn, ...]


class ScenarioError(ValueError):
    pass


def turn_from_dict(raw: Any) -> Turn:
    if not isinstance(raw, dict):
        raise ScenarioError(f"a turn is an object, got {raw!r}")
    unknown = set(raw) - {"text", "tool_calls", "usage", "gate"}
    if unknown:
        raise ScenarioError(f"unknown turn field(s) {sorted(unknown)}")
    calls = []
    for call in raw.get("tool_calls", []):
        if not isinstance(call, dict) or not isinstance(call.get("name"), str) or set(call) - {"name", "input"}:
            raise ScenarioError(f"a tool call is {{name, input}}, got {call!r}")
        calls.append(ToolCall(call["name"], dict(call.get("input", {}))))
    try:
        usage = Usage(**raw.get("usage", {}))
    except TypeError as err:
        raise ScenarioError(f"bad usage: {err}") from err
    try:
        return Turn(text=raw.get("text", ""), tool_calls=tuple(calls), usage=usage, gate=str(raw.get("gate", "")))
    except ValueError as err:
        raise ScenarioError(str(err)) from err


def scenario_from_dict(raw: Any) -> Scenario:
    if not isinstance(raw, dict) or not isinstance(raw.get("turns"), list):
        raise ScenarioError("a scenario is {name, turns: [...]}")
    return Scenario(
        name=str(raw.get("name", "unnamed")),
        turns=tuple(turn_from_dict(t) for t in raw["turns"]),
    )


def scenario_to_dict(scenario: Scenario) -> dict[str, Any]:
    return {
        "name": scenario.name,
        "turns": [
            {
                "text": t.text,
                "tool_calls": [{"name": c.name, "input": c.input} for c in t.tool_calls],
                "usage": vars(t.usage),
                "gate": t.gate,
            }
            for t in scenario.turns
        ],
    }
