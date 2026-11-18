from __future__ import annotations

from dataclasses import dataclass
from typing import Any


@dataclass
class Outgoing:
    token: int
    kind: str
    text: str = ""
    turn: int | None = None
    reason: str = ""
    work_seq: int = 0
    repo: dict[str, Any] | None = None
    # assistant: the model message the text belongs to ("" when the harness gives none).
    block: str = ""
    # activity: {"call_id", "tool", "line", "status"}.
    activity: dict[str, str] | None = None
    # context: the input side of the newest turn's usage.
    context_tokens: int = 0

    def wire(self, host_seq: int) -> dict[str, Any]:
        event: dict[str, Any] = {"host_seq": host_seq, "kind": self.kind}
        if self.turn is not None:
            event["turn"] = self.turn
        if self.kind == "assistant":
            event["text"] = self.text
            if self.block:
                event["block"] = self.block
        elif self.kind == "activity":
            event["activity"] = self.activity
        elif self.kind == "context":
            event["context_tokens"] = self.context_tokens
        elif self.kind == "ready":
            event["work_seq"] = self.work_seq
        elif self.kind == "failed":
            event["reason"], event["work_seq"] = self.reason, self.work_seq
        elif self.kind == "unsupported":
            event["reason"] = self.reason
        elif self.kind == "repo_state":
            # The start the session is on, so that a report from an earlier
            # session does not replace a later one's.
            event["repo"], event["work_seq"] = self.repo, self.work_seq
        return event
