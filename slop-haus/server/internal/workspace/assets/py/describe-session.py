"""slop-haus session description, for the hook scripts.

Reads a hook payload on stdin and writes it to stdout with a `slop_haus`
object added: which harness this run is, at which version, on which
provider and model, and the labels its owner set in run.env. The server
records it against the session and shows it on the Agents screen.

The object also carries `layout`, what this workspace looks like on disk:
whether its root is a git repo and its `origin`, and the same for each
top-level directory that is one. The Workspace screen shows it. Only the
root and its immediate children are looked at, only the ones that are git
repos are reported -- never a listing of the root -- and a remote is sent
with any credential in it removed (`https://user:token@host/x` goes as
`host/x`).

Every field comes from something that already knows it, so nobody types
anything in:

- SLOP_HAUS_HARNESS, exported by run_remote_control.sh;
- SLOP_HAUS_HARNESS_VERSION, SLOP_HAUS_PROVIDER, SLOP_HAUS_MODEL, set by a
  harness extension that knows them (pi's does);
- the payload's own `model` (Claude Code's SessionStart), or for Claude
  Code's Stop, which carries none, the model and version recorded in the
  tail of its session transcript;
- SLOP_HAUS_LABELS, from run.env: `key=value` or bare `key` items,
  comma-separated;
- .slop-haus/bundle-hash, which setup and update write: the hash of the
  managed asset bundle this workspace installed, reported as `bundle_hash`
  so the Agents screen can mark a workspace that has not updated;
- SLOP_HAUS_CONFIG_REVISION, exported by run_remote_control.sh: the revision of the
  harness config slop-haus handed this run, which the Agents screen
  compares with the current one.

It is decoration on a hook whose real job is elsewhere, so it never fails
one: anything it cannot make sense of is left out, and a payload it cannot
parse is passed through untouched. The server sanitises what arrives; this
is not a trust boundary and nothing may treat it as one.

Installed into a workspace by setup and replaced by every update.
"""

import json
import os
import subprocess
import sys
import time

# How much of a transcript's end to read. The last assistant message is
# near the end, and a transcript can run to hundreds of megabytes.
TRANSCRIPT_TAIL_BYTES = 256 * 1024


# A workspace is a handful of repos; past this the rest are left out
# rather than spawning a git process per directory on every hook.
MAX_LAYOUT_REPOS = 50
# Inside the 10s the SessionStart hook is given, with room for the request.
LAYOUT_BUDGET_SECONDS = 4


class LayoutUnavailable(Exception):
    """git could not answer, so the layout cannot be told truthfully."""


def strip_userinfo(remote):
    """The remote without anything that could be a credential.

    Everything up to the last `@` goes, not just up to the end of the
    authority: a password may hold an unescaped `/`, `?` or `#`, which would
    end the authority early and leave part of it behind. Over-stripping a path that
    holds an `@` is the safe way to be wrong. A query or fragment goes too,
    and the scheme with it, so `https://h/x` and `git@h:x` compare alike
    on the server.
    """
    remote = remote.strip()
    # Before the query is cut: a password may hold an unescaped `?` or `#`.
    if "@" in remote:
        remote = remote[remote.rfind("@") + 1 :]
    for mark in ("?", "#"):
        remote = remote.split(mark, 1)[0]
    scheme, sep, rest = remote.partition("://")
    if sep and scheme.replace("+", "").replace("-", "").replace(".", "").isalnum():
        remote = rest
    return remote


def git_origin(directory, deadline):
    remaining = deadline - time.monotonic()
    if remaining <= 0:
        raise LayoutUnavailable("out of time")
    try:
        result = subprocess.run(
            ["git", "-C", directory, "config", "--local", "--get", "remote.origin.url"],
            capture_output=True,
            timeout=remaining,
            check=False,
        )
    except (OSError, subprocess.SubprocessError) as err:
        raise LayoutUnavailable(str(err))
    if result.returncode == 1:
        # git's answer for a key that is not set: a repo with no origin.
        # --local, so a global or system config's remote is not mistaken
        # for the repo's own.
        return ""
    if result.returncode != 0:
        # Anything else is git failing -- a repo owned by someone else
        # ("dubious ownership"), say -- not an absent origin, and saying
        # "no origin" for it would be a false report.
        raise LayoutUnavailable("git exited %d" % result.returncode)
    return strip_userinfo(result.stdout.decode("utf-8", "replace"))


def is_git_repo(directory):
    # A file, not a directory, in a worktree or a submodule.
    return os.path.lexists(os.path.join(directory, ".git"))


def workspace_layout(root):
    """What the workspace root and its top-level git repos look like, or
    None when git could not say: no report is better than a wrong one, and
    the server keeps the last it had."""
    deadline = time.monotonic() + LAYOUT_BUDGET_SECONDS
    try:
        root_is_git = is_git_repo(root)
        layout = {
            "root": {"path": ".", "git": root_is_git, "origin": git_origin(root, deadline) if root_is_git else ""}
        }
        repos = []
        try:
            names = sorted(os.listdir(root))
        except OSError:
            names = []
        for name in names:
            # Hidden directories are ours or the harness's (.slop-haus,
            # .claude); a symlink may point anywhere, so it is not followed.
            if name.startswith(".") or len(repos) >= MAX_LAYOUT_REPOS:
                continue
            path = os.path.join(root, name)
            if os.path.islink(path) or not os.path.isdir(path) or not is_git_repo(path):
                continue
            repos.append({"path": name, "git": True, "origin": git_origin(path, deadline)})
        layout["repos"] = repos
        return layout
    except LayoutUnavailable:
        return None


def parse_labels(raw):
    labels = {}
    for item in raw.split(","):
        key, _, value = item.partition("=")
        key = key.strip()
        if key:
            labels[key] = value.strip()
    return labels


def from_transcript(path):
    """The model and harness version of a Claude Code transcript's latest
    assistant message, read from the end of the file."""
    model = version = ""
    try:
        with open(path, "rb") as f:
            f.seek(0, os.SEEK_END)
            size = f.tell()
            f.seek(max(0, size - TRANSCRIPT_TAIL_BYTES))
            tail = f.read().decode("utf-8", "replace")
    except (OSError, TypeError, ValueError):
        return model, version
    for line in reversed(tail.splitlines()):
        try:
            entry = json.loads(line)
        except ValueError:
            continue
        if not isinstance(entry, dict):
            continue
        if not version and isinstance(entry.get("version"), str):
            version = entry["version"]
        message = entry.get("message")
        if not model and entry.get("type") == "assistant" and isinstance(message, dict):
            candidate = message.get("model")
            # Claude Code writes its own error notices as "<synthetic>".
            if isinstance(candidate, str) and not candidate.startswith("<"):
                model = candidate
        if model and version:
            break
    return model, version


def workspace_root():
    # This script is installed at <root>/.slop-haus/describe-session.py.
    return os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


# A hash is 16 characters today; this leaves room without reading a whole file.
MAX_BUNDLE_HASH = 64


def read_bundle_hash(root):
    """The bundle hash setup or update recorded, or "" when there is none."""
    try:
        with open(os.path.join(root, ".slop-haus", "bundle-hash"), encoding="ascii") as f:
            text = f.read(MAX_BUNDLE_HASH + 1).strip()
    except (OSError, UnicodeDecodeError):
        return ""
    # A hash is hex; anything else is a damaged file, not a version.
    if text and len(text) <= MAX_BUNDLE_HASH and all(c in "0123456789abcdef" for c in text):
        return text
    return ""


def describe(payload, env):
    existing = payload.get("slop_haus")
    description = dict(existing) if isinstance(existing, dict) else {}

    def put(key, value):
        if value and not description.get(key):
            description[key] = value

    put("harness", env.get("SLOP_HAUS_HARNESS", ""))
    put("harness_version", env.get("SLOP_HAUS_HARNESS_VERSION", ""))
    put("provider", env.get("SLOP_HAUS_PROVIDER", ""))
    put("model", env.get("SLOP_HAUS_MODEL", ""))

    model = payload.get("model")
    if isinstance(model, dict):
        model = model.get("id")
    if isinstance(model, str):
        put("model", model)

    transcript = payload.get("transcript_path")
    if isinstance(transcript, str) and transcript and not (description.get("model") and description.get("harness_version")):
        transcript_model, transcript_version = from_transcript(transcript)
        put("model", transcript_model)
        put("harness_version", transcript_version)

    revision = env.get("SLOP_HAUS_CONFIG_REVISION", "")
    if revision.isascii() and revision.isdigit():
        put("config_revision", revision)

    put("bundle_hash", read_bundle_hash(workspace_root()))

    labels = parse_labels(env.get("SLOP_HAUS_LABELS", ""))
    if labels and not isinstance(description.get("labels"), dict):
        description["labels"] = labels

    layout = workspace_layout(workspace_root())
    if layout is not None:
        description["layout"] = layout

    if not description:
        return None
    payload["slop_haus"] = description
    return payload


def main():
    raw = sys.stdin.buffer.read()
    try:
        payload = json.loads(raw)
        if not isinstance(payload, dict):
            raise ValueError("not an object")
        described = describe(payload, os.environ)
        out = raw if described is None else json.dumps(described).encode()
    except Exception:
        out = raw
    sys.stdout.buffer.write(out)


if __name__ == "__main__":
    main()
