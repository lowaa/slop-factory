from __future__ import annotations

import re

CONTROL = re.compile(r"[\x00-\x08\x0b-\x1f\x7f]")
URL_USERINFO = re.compile(r"(://)[^/@\s]+@")
# The server refuses a repo-state field holding any character below 0x20, tab
# and newline included, and drops the batch it came in.
LINE_BREAKS = re.compile(r"[\t\n]")


def clean_text(text: str, limit: int | None = None) -> str:
    """Text safe to show the owner and to send: no control characters (newlines
    stay) and no credential in a URL (git echoes remote URLs, and some hold a
    token), and at most limit bytes when there is one (a cut never splits a character, and
    leaves no marker: the limits are the server's wire limits)."""
    text = URL_USERINFO.sub(r"\1***@", CONTROL.sub("", text))
    return text if limit is None else truncate_bytes(text, limit)


def clean_field(text: str, limit: int) -> str:
    """clean_text for a single-line repo-state field: tab and newline, which
    git allows in a path or a subject, become spaces."""
    return clean_text(LINE_BREAKS.sub(" ", text), limit)


def truncate_bytes(text: str, limit: int) -> str:
    raw = text.encode("utf-8")
    if len(raw) <= limit:
        return text
    return raw[:limit].decode("utf-8", errors="ignore")


def split_bytes(text: str, limit: int) -> list[str]:
    """Pieces of text, each at most limit bytes, split on character boundaries."""
    pieces: list[str] = []
    while text:
        piece = truncate_bytes(text, limit)
        pieces.append(piece)
        text = text[len(piece) :]
    return pieces
