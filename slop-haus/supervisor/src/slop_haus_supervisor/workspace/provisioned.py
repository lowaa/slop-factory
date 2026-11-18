from __future__ import annotations

import logging
import os
import re
import shlex
import subprocess
import tempfile
from collections.abc import Callable
from enum import Enum
from pathlib import Path
from typing import Protocol, runtime_checkable

from slop_haus_supervisor.ports import ProcessRunner
from slop_haus_supervisor.harness.text import SecretMasker
from slop_haus_supervisor.slophaus.credentials import TokenPair, credentials_lock, read_credentials, write_credentials, write_credentials_locked
from slop_haus_supervisor.slophaus.token import SlotToken
from slop_haus_supervisor.slophaus.transport import ApiError
from slop_haus_supervisor.text import clean_text
from slop_haus_supervisor.workspace.config import ConfigError
from slop_haus_supervisor.workspace.harness_config import FETCH_TIMEOUT, HarnessConfigStore
from slop_haus_supervisor.workspace.update_script import REPOS_HEADER, UpdateScript

# Runs a command with the terminal attached (the setup script's device sign-in and clone prompts need it)
# and returns its exit status.
Interactive = Callable[[list[str], Path], int]

# update.sh fetches for 30s at most; a hang must not hold a slot (or its lease) for longer than this.
REFRESH_TIMEOUT = 90.0
# What the server answers a refresh with when the credential is gone for good (invalid_grant is 400, a revoked grant 401/403).
CREDENTIAL_DEAD = (400, 401, 403)
UPDATER = Path(".slop-haus") / "update.sh"
# token.sh's words when the server answered a refresh with invalid_grant (slop_haus_token.Refresher._reject): the
# grant is gone for good. Other failures (unreachable, 5xx) say nothing about the grant and never match.
CREDENTIAL_REJECTED = "credentials were rejected"
PROBE_TIMEOUT = 90.0

_SYNC_REPOS_MENTION = re.compile(r'^"\$workspace"/\.slop-haus/sync-repos\.sh', re.MULTILINE)


class Kind(Enum):
    SLOT = "slot"
    STEWARD = "steward"
    REMOTE_CONTROL = "remote_control"


class Enrollment(Protocol):
    """How the server signs a directory in: what makes the identity differs by kind, what follows does not."""

    server: str

    def issue(self) -> TokenPair:
        """Create the identity if it is not there yet and issue its credential."""
        ...

    def update_script(self, tokens: TokenPair) -> str: ...


@runtime_checkable
class ReusableEnrollment(Enrollment, Protocol):
    """An enrollment whose credential outlives a run, so it can be refreshed in place or ended when replaced."""

    def refresh(self, tokens: TokenPair) -> TokenPair:
        """Trades the pair for a new one; the old is spent."""
        ...

    def revoke(self, tokens: TokenPair) -> None:
        """Ends the grant the pair belongs to."""
        ...


def manifest_repos(text: str, label: Path | str) -> list[tuple[str, str]]:
    """The manifest's `(path, remote)` pairs, as the server listed them beside the script.

    A script that syncs repos without a list is an error: the script's text is not parsed for them.
    """
    if isinstance(text, UpdateScript) and text.repos is not None:
        return list(text.repos)
    if _SYNC_REPOS_MENTION.search(text):
        raise ConfigError(f"{label}: the server sent no repo list ({REPOS_HEADER}) with a script that syncs repos")
    return []


def _git_failures(stderr: str) -> str:
    """git's own error lines (`fatal: ...`), once each, in the order they came."""
    lines = (line.strip() for line in stderr.splitlines())
    return "; ".join(dict.fromkeys(line for line in lines if line.startswith(("fatal:", "error:"))))


def _origin_matches(clone: Path, remote: str) -> bool:
    url = subprocess.run(["git", "-C", str(clone), "config", "--get", "remote.origin.url"], capture_output=True, text=True, check=False).stdout
    # On a segment boundary, so "evilgithub.com/acme/app" is not "github.com/acme/app".
    return url.strip().removesuffix(".git").replace(":", "/").endswith(("/" + remote, "@" + remote))


class Provisioned:
    """A directory that is an agent's home: `.slop-haus/credentials.json`, token.sh and the files update.sh keeps current.

    `kind` selects only whether the manifest's repos are cloned into it (not for the steward).
    `enrollment` is needed to `ensure` or `install`; a unit for an existing directory can `refresh` without one.
    """

    def __init__(
        self,
        directory: Path,
        kind: Kind,
        runner: ProcessRunner,
        logger: logging.Logger,
        enrollment: Enrollment | None = None,
        run: Interactive | None = None,
        refresh_timeout: float = REFRESH_TIMEOUT,
        masker: SecretMasker | None = None,
    ) -> None:
        self._masker = masker or SecretMasker()
        self._directory = directory
        self._kind = kind
        self._runner = runner
        self._log = logger
        self._enrollment = enrollment
        self._run = run
        self._refresh_timeout = refresh_timeout
        # Set when a replacement credential was no better than the one it replaced: no more are issued until
        # the supervisor restarts, so a server that rejects every credential cannot be minted against forever.
        self._gave_up_recovering = False

    @property
    def directory(self) -> Path:
        return self._directory

    @property
    def token_script(self) -> Path:
        return self._directory / ".slop-haus" / "token.sh"

    def ensure(self, protocol_for: Callable[[str], str | None] | None = None) -> Path:
        """The credential is issued once; token.sh owns refreshing it, so this is cheap once it is in place.

        `protocol_for` picks the clone protocol from the script, which is only known after it is fetched.
        A slot is always set up afresh: a directory left behind by a removed slot holds a stale credential.
        Any other kind keeps the credential it has: when only the helper files are missing, or the last install's
        repos did not check out, they are laid down again with it, and a new one is issued (and the old grant ended)
        only when the old one is dead.
        """
        if self._kind is Kind.REMOTE_CONTROL:
            # Sessions work in the manifest's clones. A workspace made before they were cloned still has the marker,
            # and is installed again for them: a complete one would return below without ever reaching the install.
            no_clone = self._directory / ".slop-haus" / "no-clone"
            if no_clone.exists():
                self._clones_pending.touch()
                no_clone.unlink()
        if self._kind is Kind.SLOT:
            return self._enrol(protocol_for)
        existing = read_credentials(self._directory)
        if existing is None:
            return self._enrol(protocol_for)
        if self.token_script.is_file() and not self._clones_pending.exists():
            return self._recover_if_rejected(protocol_for)
        return self._repair(protocol_for)

    @property
    def _clones_pending(self) -> Path:
        """Left by an install whose repos did not check out: the helper is in place, so without it nothing installs again."""
        return self._directory / ".slop-haus" / "clones-pending"

    def _recover_if_rejected(self, protocol_for: Callable[[str], str | None] | None) -> Path:
        """Replaces a credential the server has ended, which token.sh reports when it tries to refresh it.

        Nothing is revoked: the grant is already dead. Without an enrollment there is nothing to replace it
        with, so the helper's own error stands.
        """
        if self._enrollment is None or not self._credential_rejected():
            return self.token_script
        if self._gave_up_recovering:
            raise ConfigError(f"{self._directory.name}: its credential was rejected and its replacement was too; not issuing more until the supervisor restarts")
        self._log.warning("%s: the server rejected its credential; issuing a new one", self._directory.name)
        self._gave_up_recovering = True
        script = self._enrol(protocol_for)
        # Only a replacement that works earns another recovery later: this is what stops a loop of dead grants.
        if self._credential_rejected():
            raise ConfigError(f"{self._directory.name}: the credential issued to replace a rejected one was rejected too")
        self._gave_up_recovering = False
        return script

    def _credential_rejected(self) -> bool:
        try:
            done = self._runner.run([str(self.token_script)], self._directory, PROBE_TIMEOUT)
        except (OSError, subprocess.SubprocessError):
            return False
        return done.returncode != 0 and CREDENTIAL_REJECTED in done.stderr

    def _repair(self, protocol_for: Callable[[str], str | None] | None) -> Path:
        enrollment = self._need_reusable()
        # Held across the re-read, the refresh and the write: a refresh token presented twice outside the server's
        # grace ends the grant, so whoever else refreshes (token.sh) must see this one's pair or wait for it.
        # Not held through the install, whose update.sh may run token.sh and would wait on us.
        with credentials_lock(self._directory):
            existing = read_credentials(self._directory)
            if existing is None:
                dead: TokenPair | None = None
                tokens = None
            else:
                dead = existing
                try:
                    tokens = enrollment.refresh(existing)
                except ApiError as err:
                    if err.status not in CREDENTIAL_DEAD:
                        # Not knowing is not a reason to mint: the grant may be fine and the next try finds out.
                        raise
                    self._log.warning(
                        "%s: its credential is no longer accepted (%s); issuing a new one", self._directory.name, self._clean(str(err))
                    )
                    tokens = None
                else:
                    # The refresh spent the old pair: store the new one before anything else can fail.
                    write_credentials_locked(self._directory, enrollment.server, tokens)
        if tokens is None:
            return self._enrol(protocol_for, replacing=dead)
        self._install_for(enrollment, tokens, protocol_for)
        return self.token_script

    def _enrol(self, protocol_for: Callable[[str], str | None] | None, replacing: TokenPair | None = None) -> Path:
        enrollment = self._need_enrollment()
        tokens = enrollment.issue()
        self._directory.mkdir(parents=True, exist_ok=True)
        write_credentials(self._directory, enrollment.server, tokens)
        if replacing is not None:
            self._revoke(replacing)
        self._install_for(enrollment, tokens, protocol_for)
        return self.token_script

    def _install_for(self, enrollment: Enrollment, tokens: TokenPair, protocol_for: Callable[[str], str | None] | None) -> None:
        script = enrollment.update_script(tokens)
        # update.sh refuses a directory that was never set up, whatever the kind; this is what says it was.
        env_file = self._directory / ".slop-haus.env"
        if not env_file.exists():
            env_file.write_text(f"SLOP_HAUS_AGENT={shlex.quote(self._directory.name)}\n")
        marker = self._directory / ".slop-haus" / "no-clone"
        if self._kind is Kind.STEWARD:
            # Nothing to work on here; keep the update from cloning the manifest into it.
            marker.parent.mkdir(parents=True, exist_ok=True)
            marker.touch()
        self.install(script, protocol_for(script) if protocol_for else None)

    def _revoke(self, old: TokenPair) -> None:
        """Ends the grant a replaced credential belonged to, so it does not outlive the file that held it."""
        enrollment = self._need_reusable()
        try:
            enrollment.revoke(old)
        except ApiError as err:
            # Already gone (403) is the common case here, which is why it was replaced. Anything else leaves a live
            # grant nobody holds the token for, which stays live until its refresh token expires, and that bounds it.
            level = logging.INFO if err.status == 403 else logging.WARNING
            self._log.log(level, "%s: the replaced credential's grant was not revoked: %s", self._directory.name, self._clean(str(err)))

    def install(self, script: str, protocol: str | None = None) -> None:
        """Run the update script; `protocol` is handed to its sync-repos call, which otherwise only guesses from existing clones.

        The script tolerates a failed clone (sync-repos exits 1 and the script carries on), so afterwards every
        repo the manifest names must be a checkout of its remote, or this fails naming it.
        """
        with tempfile.TemporaryDirectory() as scratch:
            path = Path(scratch) / "update.sh"
            path.write_text(script)
            argv = ["bash", str(path)]
            if protocol:
                argv = ["env", f"SLOP_HAUS_CLONE_PROTOCOL={protocol}", *argv]
            if self._run is not None:
                status, detail = self._run(argv, self._directory), ""
            else:
                done = self._runner.run(argv, self._directory)
                status, detail = done.returncode, done.stderr.strip()
        if status != 0:
            raise ConfigError(f"installing the workspace files failed in {self._directory}" + (f": {detail}" if detail else ""))
        # A slot's update.sh is the server's own script, which lays down token.sh; only the others rely on it here.
        if self._kind is not Kind.SLOT and not self.token_script.is_file():
            raise ConfigError(f"installing the workspace files did not leave a token helper in {self._directory}")
        try:
            self._verify_repos(script, detail)
        except ConfigError:
            self._clones_pending.parent.mkdir(parents=True, exist_ok=True)
            self._clones_pending.touch()
            raise
        self._clones_pending.unlink(missing_ok=True)

    def refresh(self) -> None:
        """So briefing changes on main reach runs; a failed refresh leaves the files already there."""
        try:
            result = self._runner.run([str(self._directory / UPDATER)], self._directory, self._refresh_timeout)
        except (OSError, subprocess.SubprocessError) as err:
            self._log.warning("%s: briefing refresh did not run: %s", self._directory.name, self._clean(f"{type(err).__name__}: {err}"))
            return
        if result.returncode != 0:
            self._log.warning("%s: briefing refresh failed (status %d)", self._directory.name, result.returncode)

    def harness_config(self, timeout: float = FETCH_TIMEOUT) -> HarnessConfigStore:
        """This agent's harness config, as slop-haus holds it; the saved copy stands in when it cannot be fetched."""
        try:
            server = (self._directory / ".slop-haus" / "server-url").read_text().partition("\n")[0].strip()
        except OSError:
            server = ""
        return HarnessConfigStore(self._directory, server, SlotToken(self.token_script, self._runner).get, self._log, timeout)

    def _need_reusable(self) -> ReusableEnrollment:
        enrollment = self._need_enrollment()
        if not isinstance(enrollment, ReusableEnrollment):
            raise ConfigError(f"{self._directory}'s enrollment cannot refresh or revoke a credential")
        return enrollment

    def _clean(self, text: str) -> str:
        return clean_text(self._masker.mask(text), 300)

    def _need_enrollment(self) -> Enrollment:
        if self._enrollment is None:
            raise ConfigError(f"{self._directory} cannot be signed in without the server")
        return self._enrollment

    def _verify_repos(self, script: str, stderr: str = "") -> None:
        if os.environ.get("SLOP_HAUS_CLONE", "").lower() in ("no", "n", "0", "false", "off") or (self._directory / ".slop-haus" / "no-clone").exists():
            return
        missing = [
            f"{remote} at {self._directory / path}"
            for path, remote in manifest_repos(script, "the slot's workspace files")
            if not (self._directory / path / ".git").exists() or not _origin_matches(self._directory / path, remote)
        ]
        if missing:
            # sync-repos reports a failed clone and carries on, so update.sh exits 0 and git's own reason is only on
            # stderr. It leads the message: the board shows the first 160 characters of it.
            why = _git_failures(stderr)
            raise ConfigError(
                (f"{why}: " if why else "")
                + "the slot is not a checkout of " + ", ".join(missing) + " -- check this machine's git access to it "
                "(an ssh key without a passphrase or an ssh-agent, or an https credential helper), "
                "or set SLOP_HAUS_CLONE_PROTOCOL=ssh|https"
            )
