"""Where the stack is, from the environment test.sh's compose file sets."""

from __future__ import annotations

import os
from dataclasses import dataclass
from pathlib import Path


@dataclass(frozen=True)
class Stack:
    server: str
    model_server: str
    email: str
    password: str
    harness: str
    # The version the image was built with, or "latest" for the scheduled
    # run, which has no pin to compare against.
    harness_version: str
    logs: Path


def stack_from_env() -> Stack:
    def need(name: str) -> str:
        value = os.environ.get(name)
        if not value:
            raise RuntimeError(f"{name} is not set -- run this through harness_e2e/test.sh")
        return value

    return Stack(
        server=need("HARNESS_E2E_SERVER"),
        model_server=need("HARNESS_E2E_MODEL_SERVER"),
        email=need("HARNESS_E2E_EMAIL"),
        password=need("HARNESS_E2E_PASSWORD"),
        harness=need("HARNESS"),
        harness_version=need("HARNESS_VERSION"),
        logs=Path(os.environ.get("HARNESS_E2E_LOGS", "/logs")),
    )
