from __future__ import annotations

import json
from dataclasses import dataclass
from pathlib import Path
from typing import Any

from slop_haus_supervisor.harness.text import as_dict
from slop_haus_supervisor.usage.conversation import as_float, as_int

# go-llm-usage's two billing modes: a subscription's cost is not money spent, a metered one's is.
SUBSCRIPTION = "subscription"
METERED = "metered"


def claude_billing_mode(api_key_source: object) -> str:
    """Claude Code's init record says where its credential came from; an OAuth login is a
    subscription and an ANTHROPIC_API_KEY pays per token. Never the credential itself."""
    return SUBSCRIPTION if api_key_source in (None, "none") else METERED


def pi_oauth_login(agent_dir: Path, provider: str) -> bool:
    """Whether pi in `agent_dir` is signed in to the provider with OAuth. Reads the kind of login, never the key."""
    try:
        with open(agent_dir / "auth.json", encoding="utf-8") as f:
            entry = as_dict(json.load(f).get(provider))
    except (OSError, ValueError, AttributeError):
        return False
    return entry is not None and entry.get("type") == "oauth"


def pi_billing_mode(agent_dir: Path, provider: str) -> str:
    """Subscription when pi signed in to the provider with OAuth, where the cost it estimates
    is not what is charged; otherwise metered."""
    return SUBSCRIPTION if pi_oauth_login(agent_dir, provider) else METERED


def pi_counters(stats: dict[str, Any]) -> dict[str, int]:
    """pi's `get_session_stats` as usage counters: totals for the whole session."""
    tokens = as_dict(stats.get("tokens")) or {}
    cost = as_float(stats.get("cost"))
    return {
        "requests": as_int(stats.get("assistantMessages")),
        "input": as_int(tokens.get("input")), "output": as_int(tokens.get("output")),
        "cache_read": as_int(tokens.get("cacheRead")), "cache_write": as_int(tokens.get("cacheWrite")),
        "cost_micros": int(round(cost * 1e6)) if cost > 0 else 0,
    }


def counter_change(now: dict[str, int], before: dict[str, int]) -> dict[str, int]:
    return {name: max(value - as_int(before.get(name)), 0) for name, value in now.items()}


def codex_billing_mode(auth_mode: object) -> str:
    """Codex's auth.json says how it was signed in: a ChatGPT login is a subscription, an API
    key (`apikey`) or anything unknown pays per token. Reads the kind of login, never the key."""
    return SUBSCRIPTION if auth_mode == "chatgpt" else METERED


@dataclass(frozen=True)
class CodexLogin:
    """What Codex's auth.json says about the login, without the credential: the kind, and whether tokens are there."""

    auth_mode: str = ""
    has_tokens: bool = False


def read_codex_login(config_dir: Path) -> CodexLogin:
    try:
        auth = as_dict(json.loads((config_dir / "auth.json").read_text(encoding="utf-8"))) or {}
    except (OSError, ValueError):
        return CodexLogin()
    mode = auth.get("auth_mode")
    return CodexLogin(mode if isinstance(mode, str) else "", bool(auth.get("tokens")))


def codex_login_billing_mode(config_dir: Path) -> str:
    return codex_billing_mode(read_codex_login(config_dir).auth_mode)
