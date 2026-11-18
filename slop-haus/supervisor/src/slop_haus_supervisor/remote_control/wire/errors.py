from __future__ import annotations


class Refused(Exception):
    """A step the supervisor will not take, with the reason to show the owner."""


class ServerError(Exception):
    def __init__(self, status: int, detail: str) -> None:
        super().__init__(f"slop-haus answered {status}: {detail}")
        self.status = status
        self.detail = detail


class Unreachable(Exception):
    pass
