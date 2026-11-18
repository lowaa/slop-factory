"""Failure texts the harnesses classify, and where each came from.

CAPTURED are copied from a real `claude` 2.1.303 run. READ_FROM_BINARY are message templates read
out of the 2.1.303 binary (and its stream-json schema), not seen live: a usage limit cannot be
provoked on demand. Worth replacing with captures when one is next hit.
"""

from __future__ import annotations

from dataclasses import dataclass

from slop_haus_supervisor.harness.base import OutcomeKind

# 2025-10-19 10:00:00 UTC, so "resets 3pm" has a known next occurrence.
NOW = 1_760_868_000.0


@dataclass(frozen=True)
class Sample:
    name: str
    text: str
    kind: OutcomeKind | None
    reset_at: float | None = None


CAPTURED = [
    Sample("not logged in", "Not logged in · Please run /login", OutcomeKind.AUTH_FAILED),
]

READ_FROM_BINARY = [
    Sample("usage limit, legacy epoch", "Claude AI usage limit reached|1760900000", OutcomeKind.USAGE_LIMIT, 1_760_900_000.0),
    Sample("usage limit, reset past", "Claude AI usage limit reached|1760000000", OutcomeKind.USAGE_LIMIT),
    Sample("usage limit, continues", "Usage limit reached · continues automatically when it resets", OutcomeKind.USAGE_LIMIT),
    Sample("hit your limit", "You've hit your limit · resets 3pm (America/New_York)", OutcomeKind.USAGE_LIMIT, 1_760_900_400.0),
    Sample("hit your monthly spend limit", "You've hit your monthly spend limit.", OutcomeKind.USAGE_LIMIT),
    Sample("team budget", "You've hit your team's shared budget. /model to switch models.", OutcomeKind.USAGE_LIMIT),
    Sample("out of credits", "You're out of usage credits.", OutcomeKind.USAGE_LIMIT),
    Sample("invalid key", "Invalid API key · Please run /login", OutcomeKind.AUTH_FAILED),
    Sample("api 401", 'API Error: 401 {"type":"error","error":{"type":"authentication_error"}}', OutcomeKind.AUTH_FAILED),
    Sample("credit balance", "Credit balance is too low", OutcomeKind.AUTH_FAILED),
    Sample("overloaded", "API Error: 529 overloaded_error", OutcomeKind.TRANSIENT),
    Sample("server error", "API Error: 500 Internal server error", OutcomeKind.TRANSIENT),
    Sample("rate limited 429", "API Error: 429 rate_limit_error", OutcomeKind.TRANSIENT),
    Sample("network", "API Error: Unable to connect to API (ECONNREFUSED)", OutcomeKind.TRANSIENT),
    Sample("model said something else", "I could not find the file you named.", None),
]

PI_SAMPLES = [
    Sample("pi no model", "No model selected", OutcomeKind.AUTH_FAILED),
    Sample("pi unknown provider", 'Unknown provider "acme"', OutcomeKind.AUTH_FAILED),
    Sample("pi overloaded", "529 overloaded", OutcomeKind.TRANSIENT),
]

# Captured from codex 0.165.0 against the real endpoint with no key.
CODEX_SAMPLES = [
    Sample("codex 401", "unexpected status 401 Unauthorized: Missing bearer or basic authentication in header, url: https://api.openai.com/v1/responses", OutcomeKind.AUTH_FAILED),
]

ALL = CAPTURED + READ_FROM_BINARY + PI_SAMPLES + CODEX_SAMPLES

NO_CONVERSATION = "No conversation found with session ID: 11111111-1111-4111-8111-111111111111"
