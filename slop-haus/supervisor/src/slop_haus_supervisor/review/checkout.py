"""The pushed head, checked out where the agent cannot have touched it.

A fresh clone of origin rather than a worktree of the slot's repo: a worktree shares the slot repo's
config and hooks, which the agent can write (a `core.fsmonitor` there runs on the next `git status`),
and its branch namespace, where the attempt branch is already checked out. slop-review files a run under
the checked-out branch's name, so the clone puts that name on the head.

Nothing comes from the slot repo but which manifest repo it is. Its origin URL is the agent's to rewrite:
a repo of its own could serve the head with a `main` equal to it, and the review would see an empty
diff. So the URL is rebuilt from the workspace manifest's remote. Its objects are not borrowed either
(`--reference`): git trusts an alternate's objects without hashing them, so a planted one would stand in
for the pushed content.
"""

from __future__ import annotations

import re
from collections.abc import Sequence
from dataclasses import dataclass
from pathlib import Path

from slop_haus_supervisor.ports import ProcessResult
from slop_haus_supervisor.review.processes import ProcessGroup

SHA = re.compile(r"[0-9a-f]{40}")
# A manifest remote: host/path, as the server lists it.
REMOTE = re.compile(r"[a-z0-9][a-z0-9.-]*/[A-Za-z0-9._/-]+")
_URL = re.compile(r"(?:https?|ssh)://(?:[^@/]+@)?([^/:@]+)(?::[0-9]+)?/(.+?)(?:\.git)?/?")
_SCP = re.compile(r"[^@/:]+@([^/:@]+):(?!/)(.+?)(?:\.git)?/?")
# What git takes as a branch and slop-haus names attempts with; never an option.
BRANCH = re.compile(r"[A-Za-z0-9][A-Za-z0-9._/-]*")
MAIN_REFS = ("refs/remotes/origin/main", "refs/remotes/origin/master")


def canonical_remote(url: str) -> str | None:
    """The `host/path` of a URL git clones over https or ssh; None for anything else (a local path, file://, ext::)."""
    found = _URL.fullmatch(url) or _SCP.fullmatch(url)
    if found is None:
        return None
    remote = f"{found.group(1).lower()}/{found.group(2)}"
    return remote if REMOTE.fullmatch(remote) and ".." not in remote else None


def clone_url(existing: str, remote: str) -> str:
    """`remote` the way `existing` reaches it, so the clone authenticates as the slot's own fetches do."""
    host, _, path = remote.partition("/")
    if existing.startswith(("git@", "ssh://")):
        return f"git@{host}:{path}.git"
    return f"https://{host}/{path}.git"


class NotPushed(Exception):
    pass


class CheckoutFailed(Exception):
    pass


@dataclass(frozen=True)
class Checkout:
    directory: Path
    # The head's merge-base with origin's main; None when there is none to find.
    base: str | None


class ReviewCheckout:
    def __init__(self, procs: ProcessGroup, git_timeout: float) -> None:
        self._procs = procs
        self._timeout = git_timeout

    def prepare(self, source: Path, branch: str, head: str, dest: Path, remotes: Sequence[str]) -> Checkout:
        """Clones the manifest repo `source` is a checkout of into `dest`, which must not exist, and puts `branch` on `head`."""
        head = head.lower()
        if not SHA.fullmatch(head):
            raise CheckoutFailed(f"{head[:60]!r} is not a full commit sha")
        if not BRANCH.fullmatch(branch) or ".." in branch:
            raise CheckoutFailed(f"{branch[:60]!r} is not a branch name")
        # As configured: `remote get-url` applies insteadOf rewrites, which name a mirror, not the repo.
        url = self._git(source, "config", "--get", "remote.origin.url", what="read the slot repo's origin").stdout.strip()
        remote = canonical_remote(url)
        trusted = {canonical_remote(f"https://{r}") for r in remotes}
        if remote is None or remote not in trusted:
            raise CheckoutFailed(f"the slot repo's origin ({remote or 'not a network URL'}) is not a repo of the workspace manifest")
        # Blobs are fetched as the review reads them, so a big repo costs only what the change touches.
        self._git(
            dest.parent, "-c", "protocol.ext.allow=never", "clone", "--quiet", "--no-checkout", "--no-tags",
            "--filter=blob:none", "--", clone_url(url, remote), str(dest), what="clone origin",
        )
        # Asked of origin's branch, not of the object: a commit the agent made after pushing is not the change.
        pushed = self._procs.run(
            ["git", "merge-base", "--is-ancestor", head, f"refs/remotes/origin/{branch}"], dest, self._timeout
        )
        if pushed.returncode != 0:
            raise NotPushed(f"{head} is not on origin's {branch}")
        self._git(dest, "checkout", "--quiet", "-B", branch, head, "--", what=f"check out {head}")
        # The installed .env is untracked; the review diffs untracked files too.
        exclude = dest / ".git" / "info" / "exclude"
        exclude.parent.mkdir(parents=True, exist_ok=True)
        with exclude.open("a") as out:
            out.write("\n/.env\n")
        return Checkout(dest, self._merge_base(dest))

    def _merge_base(self, dest: Path) -> str | None:
        for ref in MAIN_REFS:
            found = self._procs.run(["git", "merge-base", ref, "HEAD"], dest, self._timeout)
            if found.returncode == 0 and SHA.fullmatch(found.stdout.strip()):
                return found.stdout.strip()
        return None

    def _git(self, cwd: Path, *args: str, what: str) -> ProcessResult:
        done = self._procs.run(["git", *args], cwd, self._timeout)
        if done.returncode != 0:
            lines = [line for line in done.stderr.splitlines() if line.strip()]
            raise CheckoutFailed(f"could not {what}" + (f": {lines[-1].strip()[:200]}" if lines else ""))
        return done
