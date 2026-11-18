"""What is running in the container, as far as the suite's own user can see."""

from __future__ import annotations

from pathlib import Path


def with_environment(name: str, value: str) -> list[tuple[int, str]]:
    """The processes (pid, command line) started with name=value in their
    environment, which is inherited: a harness and everything it ran."""
    wanted = f"{name}={value}".encode()
    found = []
    for proc in Path("/proc").iterdir():
        if not proc.name.isdigit():
            continue
        try:
            environ = (proc / "environ").read_bytes().split(b"\0")
            cmdline = (proc / "cmdline").read_bytes().replace(b"\0", b" ").decode(errors="replace").strip()
        except OSError:
            # Gone since the listing, or not ours to read.
            continue
        if wanted in environ:
            found.append((int(proc.name), cmdline))
    return found
