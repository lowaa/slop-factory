"""Masking and shape-checking of what a harness hands back."""

from __future__ import annotations

from collections.abc import Iterable
from typing import Any


class SecretMasker:
    """Masks the values the supervisor holds as secrets in text bound for logs or the board."""

    # Too short to be a credential, and masking it would shred ordinary text.
    MIN_LENGTH = 4

    def __init__(self, values: Iterable[str] = ()) -> None:
        # Longest first, so a value that contains another is masked whole.
        self._values = self._sorted(values)

    @classmethod
    def _sorted(cls, values: Iterable[str]) -> list[str]:
        return sorted({v for v in values if len(v) >= cls.MIN_LENGTH}, key=len, reverse=True)

    def extend(self, values: Iterable[str]) -> None:
        """Adds values learnt after construction. Swaps the list whole, so a concurrent mask() sees the old or the new."""
        self._values = self._sorted([*self._values, *values])

    def mask(self, text: str) -> str:
        for value in self._values:
            text = text.replace(value, "***")
        return text


def as_dict(value: object) -> dict[str, Any] | None:
    return value if isinstance(value, dict) else None  # pyright: ignore[reportUnknownVariableType]


def as_list(value: object) -> list[Any]:
    return value if isinstance(value, list) else []  # pyright: ignore[reportUnknownVariableType]
