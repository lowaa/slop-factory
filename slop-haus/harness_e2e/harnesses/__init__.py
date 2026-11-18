from __future__ import annotations

from .base import HarnessAdapter
from .claude import ClaudeCode
from .codex import Codex
from .pi import Pi

# Adding a harness is an adapter here and an image stage in the Dockerfile.
ADAPTERS: dict[str, HarnessAdapter] = {adapter.name: adapter for adapter in [ClaudeCode(), Pi(), Codex()]}
