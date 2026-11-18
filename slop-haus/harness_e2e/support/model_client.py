"""The test's side of the scripted model server: load a scenario, read what it saw."""

from __future__ import annotations

import json
import urllib.request
from dataclasses import dataclass
from typing import Any

from scripted_model.scenario import Scenario, scenario_to_dict

from .waiting import wait_for


@dataclass(frozen=True)
class Recorded:
    model: str
    system: str
    tool_names: list[str]
    message_text: str
    tool_results: list[str]
    auxiliary: bool
    raw: dict[str, Any]


class ModelClient:
    def __init__(self, base_url: str) -> None:
        self.base_url = base_url.rstrip("/")

    def _call(self, method: str, path: str, body: Any = None) -> Any:
        data = None if body is None else json.dumps(body).encode()
        req = urllib.request.Request(self.base_url + path, data=data, method=method)
        with urllib.request.urlopen(req, timeout=10) as resp:
            return json.loads(resp.read())

    def wait_healthy(self) -> None:
        def probe() -> bool:
            try:
                return bool(self._call("GET", "/healthz")["ok"])
            except OSError:
                return False

        wait_for("the scripted model server to answer", probe)

    def load(self, scenario: Scenario) -> None:
        self._call("PUT", "/_scripted/scenario", scenario_to_dict(scenario))

    def reset(self) -> None:
        self._call("DELETE", "/_scripted/scenario")

    def open_gate(self, name: str) -> None:
        self._call("PUT", f"/_scripted/gates/{name}")

    def errors(self) -> list[str]:
        return self._call("GET", "/_scripted/errors")

    def remaining(self) -> int:
        return self._call("GET", "/_scripted/remaining")["remaining"]

    def conversation(self) -> list[Recorded]:
        return [
            Recorded(r["model"], r["system"], r["tool_names"], r["message_text"], r["tool_results"], r["auxiliary"], r["raw"])
            for r in self._call("GET", "/_scripted/requests")
            if not r["auxiliary"]
        ]
