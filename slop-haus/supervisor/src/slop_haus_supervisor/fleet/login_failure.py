from __future__ import annotations

from slop_haus_supervisor.harness import registry
from slop_haus_supervisor.harness.text import SecretMasker
from slop_haus_supervisor.text import LINE_BREAKS, clean_text

# The board cuts a pause reason at 512 characters, so the fix comes before the harness's own words,
# which are the part that can be long.
DETAIL_CHARS = 150

# Said first because a board user reads "login failed" as the slop-haus sign-in, which this is not.
SUBJECT = "the harness login on the supervisor's host failed (not your slop-haus sign-in)"

_FALLBACK_FIX = "fix the harness login on the supervisor's host, then `slop-haus slot unpause <slot>`"


def reason(harness: str, detail: str, masker: SecretMasker) -> str:
    """What failed, the fix, then the harness's own words so a misclassified error shows.

    The detail is masked before it is cut: a cut inside a secret would leave a fragment the masker no longer matches."""
    entry = registry.find(harness)
    fix = entry.login_fix if entry is not None else _FALLBACK_FIX
    shown = clean_text(LINE_BREAKS.sub(" ", masker.mask(detail)), DETAIL_CHARS).strip()
    text = f"{SUBJECT}. To fix: {fix}"
    return f"{text}. The harness said: {shown}" if shown else text
