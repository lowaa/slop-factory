"""The server's OpenAPI document as the supervisor's contract tests use it: route lookup, request validation and spec-shaped sample responses."""

from __future__ import annotations

import copy
import json
import re
import urllib.parse
from collections.abc import Callable
from dataclasses import dataclass
from pathlib import Path
from typing import Any, cast

from jsonschema import Draft202012Validator

SPEC_PATH = Path(__file__).resolve().parents[3] / "frontend" / "src" / "api_generated" / "openapi.json"
# The depth past which optional properties and array items are left out, so a recursive schema ends.
MAX_DEPTH = 6

Json = dict[str, Any]


def load_spec() -> Json:
    return json.loads(SPEC_PATH.read_text())


@dataclass(frozen=True)
class Operation:
    method: str
    template: str
    doc: Json

    @property
    def label(self) -> str:
        return f"{self.method} {self.template}"

    def request_schema(self) -> Json | None:
        content = self.doc.get("requestBody", {}).get("content", {})
        return content.get("application/json", {}).get("schema")

    def success(self) -> tuple[int, str, Json | None]:
        """The lowest 2xx response: its status, its content type ("" for none) and that content's schema."""
        responses: Json = self.doc["responses"]
        status = min(code for code in responses if code.startswith("2"))
        content: Json = responses[status].get("content") or {}
        if not content:
            return int(status), "", None
        content_type = next(iter(content))
        schema: Json | None = content[content_type].get("schema")
        return int(status), str(content_type), schema


class Spec:
    def __init__(self, doc: Json) -> None:
        self.doc = doc
        self._routes = [
            (method.upper(), template, re.compile("^" + re.sub(r"\\\{[^}]+\\\}", "[^/]+", re.escape(template)) + "$"))
            for template, item in doc["paths"].items()
            for method in item
            if method in ("get", "post", "put", "patch", "delete")
        ]

    def paths(self) -> list[str]:
        return list(self.doc["paths"])

    def find(self, method: str, path: str) -> Operation | None:
        """The operation for a request path (query string ignored); a literal route wins over a templated one."""
        path = path.split("?", 1)[0]
        matches = [(template, pattern) for m, template, pattern in self._routes if m == method and pattern.match(path)]
        if not matches:
            return None
        template = min(matches, key=lambda found: found[0].count("{"))[0]
        return Operation(method, template, self.doc["paths"][template][method.lower()])

    def query_errors(self, operation: Operation, path: str) -> list[str]:
        """What is wrong with the query string of `path` against the operation's declared query parameters."""
        declared: dict[str, Json] = {p["name"]: p for p in operation.doc.get("parameters", []) if p["in"] == "query"}
        sent = urllib.parse.parse_qs(urllib.parse.urlsplit(path).query, keep_blank_values=True)
        found = [f"query parameter {name!r} is not in the spec" for name in sent if name not in declared]
        found += [f"query parameter {name!r} is required" for name, p in declared.items() if p.get("required") and name not in sent]
        for name, values in sent.items():
            if name in declared:
                found += [f"query parameter {name!r}: {e}" for v in values for e in self.errors(declared[name]["schema"], _typed(v, declared[name]["schema"]))]
        return found

    def has_path(self, path: str) -> bool:
        path = path.split("?", 1)[0]
        return any(pattern.match(path) for _, _, pattern in self._routes)

    def errors(self, schema: Json, value: object) -> list[str]:
        validator = Draft202012Validator({**schema, "components": self.doc["components"]})
        found: list[Any] = list(validator.iter_errors(cast(Any, value)))  # pyright: ignore[reportUnknownMemberType]
        return [f"{'/'.join(map(str, e.absolute_path)) or '<root>'}: {e.message}" for e in found]

    def sample(self, schema: Json, depth: int = 0) -> Any:
        """A value that satisfies `schema`, with every property filled, so a parser sees each field the spec offers."""
        if "$ref" in schema:
            return self.sample(self._resolve(schema["$ref"]), depth)
        if "enum" in schema:
            return schema["enum"][0]
        for key in ("anyOf", "oneOf"):
            if key in schema:
                options = [o for o in schema[key] if o.get("type") != "null"] or schema[key]
                return self.sample(options[0], depth)
        kind: Any = schema.get("type")
        if isinstance(kind, list):
            kind = next((k for k in cast(list[str], kind) if k != "null"), "null")
        match kind:
            case "object":
                props: Json = schema.get("properties", {})
                required = set(schema.get("required", []))
                return {n: self.sample(p, depth + 1) for n, p in props.items() if n in required or depth < MAX_DEPTH}
            case "array":
                return [self.sample(schema["items"], depth + 1)] if depth < MAX_DEPTH and "items" in schema else []
            case "string":
                return _string(schema)
            case "integer":
                return max(1, schema.get("minimum", 1))
            case "number":
                return float(max(1, schema.get("minimum", 1)))
            case "boolean":
                return True
            case _:
                return None

    def _resolve(self, ref: str) -> Json:
        node: Any = self.doc
        for part in ref.removeprefix("#/").split("/"):
            node = node[part]
        return node


def _typed(raw: str, schema: Json) -> object:
    """A query value as its schema's type reads it; one that does not parse stays a string and so fails validation."""
    try:
        match schema.get("type"):
            case "integer":
                return int(raw)
            case "number":
                return float(raw)
            case "boolean":
                return {"true": True, "false": False}[raw]
            case _:
                pass
    except (ValueError, KeyError):
        pass
    return raw


def _string(schema: Json) -> str:
    return {
        "date-time": "2026-01-02T03:04:05Z",
        "uuid": "123e4567-e89b-12d3-a456-426614174000",
        "date": "2026-01-02",
    }.get(schema.get("format", ""), "x" * max(1, schema.get("minLength", 1)))


def renamed(doc: Json, schema: str, old: str, new: str) -> Json:
    """A copy of the spec in which `schema` calls its property `old` by `new`, as a server-side rename would."""
    changed = copy.deepcopy(doc)
    node = changed["components"]["schemas"][schema]
    node["properties"][new] = node["properties"].pop(old)
    node["required"] = [new if name == old else name for name in node.get("required", [])]
    return changed


Tweak = Callable[[Any], Any]
