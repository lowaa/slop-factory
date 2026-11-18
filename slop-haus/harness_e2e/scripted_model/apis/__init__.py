from __future__ import annotations

from typing import Any, Iterator, Protocol

from ..model import ModelRequest
from ..scenario import Turn


class ApiAdapter(Protocol):
    """One wire API. Adding another is a new class here and an entry in server.ADAPTERS."""

    name: str
    # POST paths this API answers on.
    paths: tuple[str, ...]

    def parse(self, body: dict[str, Any]) -> ModelRequest: ...

    def content_type(self, request: ModelRequest) -> str: ...

    def encode(self, turn: Turn, request: ModelRequest, response_id: str) -> Iterator[bytes]:
        """The response body for the turn, in whatever framing the request asked for."""
        ...

    def encode_auxiliary(self, request: ModelRequest, response_id: str) -> Iterator[bytes]: ...

    def encode_error(self, message: str) -> bytes: ...

    def summarise(self, request: ModelRequest) -> str: ...
