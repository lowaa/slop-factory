"""What a supervisor-run review found, read from slop-review's JSON, and nothing more than the resume prompt needs."""

from __future__ import annotations

import json
from dataclasses import dataclass
from enum import StrEnum
from typing import Any, cast

# The server closes a review request once supervisor-posted runs at its head cover its persona list
# (steward.SlopReviewPersonas in slop-haus/server), which is this one persona.
PERSONA = "standard"
VERDICTS = frozenset({"approve", "approve_with_comments", "request_changes", "incomplete"})
# A reason goes into a prompt and a log line, so a long stderr is cut.
MAX_ERROR = 200


class ReviewOutcome(StrEnum):
    # The pass ran; it may still have failed.
    REVIEWED = "reviewed"
    NOT_PUSHED = "not_pushed"
    NO_REVIEWER = "no_reviewer"
    # The review could not be set up, or ran out of time; nothing (or not everything) was posted.
    FAILED = "failed"
    KILLED = "killed"


@dataclass(frozen=True)
class RunResult:
    verdict: str = ""
    findings: int = 0
    incomplete: bool = False
    # slop-review prints the run's id only once the server took it; a run it could not post leaves the request open.
    reported: bool = False
    # Set when the run failed: a non-zero exit or output that is not a review.
    error: str = ""

    @property
    def failed(self) -> bool:
        return bool(self.error)


@dataclass(frozen=True)
class ReviewResult:
    outcome: ReviewOutcome
    head_sha: str
    # Set only for REVIEWED.
    run: RunResult | None = None
    detail: str = ""


def _last_line(text: str) -> str:
    # slop-review ends with its error; what comes before is progress.
    lines = [line.strip() for line in text.splitlines() if line.strip()]
    return lines[-1][:MAX_ERROR] if lines else ""


def parse_run(returncode: int, stdout: str, stderr: str) -> RunResult:
    """The run's result from review.sh's exit code and output."""
    if returncode != 0:
        why = _last_line(stderr)
        return RunResult(error=f"exit {returncode}" + (f": {why}" if why else ""))
    try:
        parsed: object = json.loads(stdout)
    except ValueError:
        return RunResult(error="its output is not JSON")
    whole = cast(dict[str, Any], parsed) if isinstance(parsed, dict) else {}
    review_obj: object = whole.get("review")
    review = cast(dict[str, Any], review_obj) if isinstance(review_obj, dict) else {}
    verdict = review.get("verdict")
    if not isinstance(verdict, str) or verdict not in VERDICTS:
        return RunResult(error="its output has no verdict")
    findings: object = review.get("findings")
    return RunResult(
        verdict=verdict,
        findings=len(cast(list[object], findings)) if isinstance(findings, list) else 0,
        incomplete=review.get("incomplete") is True,
        reported=isinstance(whole.get("report"), dict),
    )
