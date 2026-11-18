"""`KEY=value` env files (secrets.env, run.env): parsed, never sourced.

A line is data. `$(...)`, backticks and `$VAR` are kept as the literal text they are, so a file
that is wrong or hostile can only set variables.
"""

from __future__ import annotations

import fcntl
import os
import re
import tempfile
from collections.abc import Callable, Generator
from contextlib import contextmanager
from pathlib import Path

KEY = re.compile(r"[A-Za-z_][A-Za-z0-9_]*")
_EXPORT = re.compile(r"export\s+")
_BARE = re.compile(r"[A-Za-z0-9_./:@%+=,-]*")
_ESCAPES = {"n": "\n", "t": "\t", '"': '"', "\\": "\\"}
_CONTROL = re.compile(r"[\x00-\x1f\x7f]")


class EnvFileError(ValueError):
    pass


def _quoted(rest: str) -> str | None:
    """The value of a quoted `rest`, or None when the quote is never closed."""
    quote, body = rest[0], rest[1:]
    if quote == "'":
        end = body.find("'")
        return None if end < 0 else body[:end]
    out: list[str] = []
    i = 0
    while i < len(body):
        ch = body[i]
        if ch == '"':
            return "".join(out)
        if ch == "\\" and i + 1 < len(body) and body[i + 1] in _ESCAPES:
            out.append(_ESCAPES[body[i + 1]])
            i += 2
            continue
        out.append(ch)
        i += 1
    return None


def parse_line(line: str) -> tuple[str, str] | None:
    """The assignment on a line; None for a blank, a comment or anything not an assignment."""
    text = line.strip()
    if not text or text.startswith("#"):
        return None
    text = _EXPORT.sub("", text, count=1)
    name, sep, rest = text.partition("=")
    name = name.strip()
    if not sep or not KEY.fullmatch(name):
        return None
    rest = rest.strip()
    if rest[:1] in ("'", '"'):
        value = _quoted(rest)
        return None if value is None else (name, value)
    # An unquoted value ends at a comment, which must follow whitespace so `a#b` stays intact.
    return name, re.split(r"\s#", rest, maxsplit=1)[0].strip()


def parse(text: str) -> dict[str, str]:
    """Every assignment, the last of a repeated key winning; blank values are kept."""
    found: dict[str, str] = {}
    for line in text.splitlines():
        parsed = parse_line(line)
        if parsed:
            found[parsed[0]] = parsed[1]
    return found


def read(path: Path) -> dict[str, str]:
    try:
        return parse(path.read_text())
    except FileNotFoundError:
        return {}


def check_key(key: str) -> str:
    if not KEY.fullmatch(key):
        raise EnvFileError(f"{key!r} is not a variable name (letters, digits and _, not starting with a digit)")
    return key


def format_assignment(key: str, value: str) -> str:
    check_key(key)
    if _CONTROL.search(value):
        raise EnvFileError("a value cannot hold control characters, newlines included")
    if _BARE.fullmatch(value):
        return f"{key}={value}"
    if "'" not in value:
        return f"{key}='{value}'"
    escaped = value.replace("\\", "\\\\").replace('"', '\\"')
    return f'{key}="{escaped}"'


def with_value(text: str, key: str, value: str) -> str:
    """`text` with `key` set: the first assignment of it replaced in place, repeats dropped, else appended."""
    line = format_assignment(key, value)
    out: list[str] = []
    placed = False
    for old in text.splitlines():
        parsed = parse_line(old)
        if parsed and parsed[0] == key:
            if not placed:
                out.append(line)
                placed = True
            continue
        out.append(old)
    if not placed:
        out.append(line)
    return "\n".join(out) + "\n"


def without(text: str, key: str) -> tuple[str, bool]:
    kept = [old for old in text.splitlines() if not ((p := parse_line(old)) and p[0] == key)]
    return ("\n".join(kept) + "\n" if kept else ""), len(kept) != len(text.splitlines())


def write_private(path: Path, text: str) -> None:
    """Beside the target and renamed over it, so a crash never leaves half a file, 0600 throughout."""
    fd, tmp = tempfile.mkstemp(dir=path.parent, prefix=f".{path.name}-")
    try:
        with os.fdopen(fd, "w") as out:
            out.write(text)
            out.flush()
            os.fsync(out.fileno())
        os.replace(tmp, path)
    except BaseException:
        Path(tmp).unlink(missing_ok=True)
        raise


class EnvFile:
    """An env file edited locally; edits are serialised so two `secret set` runs keep both."""

    def __init__(self, path: Path) -> None:
        self.path = path

    @contextmanager
    def _locked(self) -> Generator[None]:
        with (self.path.parent / f".{self.path.name}.lock").open("a") as lock:
            fcntl.flock(lock, fcntl.LOCK_EX)
            yield

    def _text(self) -> str:
        try:
            return self.path.read_text()
        except FileNotFoundError:
            return ""

    def values(self) -> dict[str, str]:
        return parse(self._text())

    def update(self, change: Callable[[str], str]) -> None:
        """Rewrites the file's text with `change` under the same lock as `set`."""
        with self._locked():
            write_private(self.path, change(self._text()))

    def set(self, key: str, value: str) -> None:
        check_key(key)
        with self._locked():
            write_private(self.path, with_value(self._text(), key, value))

    def unset(self, key: str) -> bool:
        check_key(key)
        with self._locked():
            text, removed = without(self._text(), key)
            if removed:
                write_private(self.path, text)
            return removed
