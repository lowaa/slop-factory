"""What a slot does in git and on the ticket when a run is over, or was cut short."""

from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path

from slop_haus_supervisor.fleet.pause import CauseCheck
from slop_haus_supervisor.fleet.policy import Action
from slop_haus_supervisor.fleet.slot_log import SlotLog
from slop_haus_supervisor.ports import Alerts
from slop_haus_supervisor.slophaus.client import Claim, SlopHaus
from slop_haus_supervisor.slophaus.http_client import ApiError
from slop_haus_supervisor.workspace.handover import RepoSnapshot, SafetyPush, SlotRepo


@dataclass(frozen=True)
class PauseRequest:
    """A slot that cannot go on, and how it can tell that the cause has gone."""

    reason: str
    cause_check: CauseCheck


class Aftercare:
    def __init__(self, repo: SlotRepo, client: SlopHaus, alerts: Alerts, log: SlotLog) -> None:
        self._repo = repo
        self._client = client
        self._alerts = alerts
        self._log = log

    def save_interrupted_work(self, claim: Claim | None, repo_dir: Path) -> None:
        if claim is None or not claim.has_paths:
            return
        try:
            # Not gated on has_unpushed_work: it ignores untracked files, which this save commits.
            push = self._repo.interrupted_push(repo_dir, claim.ticket_id, claim.branch)
        except Exception as err:
            # The kill must still happen; the work stays in the slot for the next start.
            self._log.warning("saving the interrupted work of %s failed: %s", claim.branch, self._log.clean(str(err)))
            return
        if push.untracked:
            self._log.warning(
                "left out of the interrupted save of %s (secret-looking or oversized): %s",
                claim.branch,
                self._log.clean(", ".join(push.untracked)),
            )
        if push.ok:
            self._log.info("saved the interrupted run's work on %s as %s", claim.branch, push.commit[:9])
        else:
            self._log.warning(
                "could not push the interrupted run's work to %s; the commit stays in the slot: %s",
                claim.branch,
                self._log.clean(push.error),
            )

    def clear_previous_work(self, repo_dir: Path, branch: str) -> PauseRequest | None:
        """A slot with unpushed work is never reused: push it, or say it must pause (None: free to reuse)."""
        repo = self._repo
        if repo.has_unpushed_work(repo_dir, branch):
            push = repo.safety_push(repo_dir, branch)
            self._log.info("safety push of %s before reuse: %s", branch, "ok" if push.ok else "failed")
            if push.ok and push.rescue_ref:
                self._log.info("%s had diverged from origin; its work was pushed to %s", branch, push.rescue_ref)
                self._alerts.alert(f"{self._log.agent}: {branch} had diverged from origin; its work is on {push.rescue_ref}")
            if not push.ok:
                reason = f"unpushed work for {branch} could not be pushed: {push.error}"
                return PauseRequest(reason, lambda: reason if repo.has_unpushed_work(repo_dir, branch) else "")
        elif repo.drop_superseded(repo_dir, branch):
            self._log.info("local %s superseded by origin; reset", branch)
        repo.release_worktrees(repo_dir, branch)
        return None

    def check_stray_work(self, claim: Claim, repo_dir: Path, before: RepoSnapshot | None) -> None:
        """Warn only: pushing or moving another branch's commits is for a person, and the ticket stays held.

        The server takes a ticket comment from a user token only, so the supervisor's voice is the log and the alert.
        """
        if before is None or not claim.has_paths:
            return
        stray = self._repo.stray_work(repo_dir, claim.branch, before)
        if stray:
            message = f"the run on ticket #{claim.ticket_id} left work outside {claim.branch}: " + "; ".join(stray)
            self._log.warning("%s", self._log.clean(message))
            self._alerts.alert(self._log.clean(f"{self._log.agent}: {message}"))

    def hand_back(self, claim: Claim, repo_dir: Path, action: Action) -> None:
        """Pushes what was left unpushed, then tells the ticket why the supervisor gave up on it."""
        push = self._push_leftovers(claim, repo_dir)
        self._client.escalate(self._log.agent, _escalation_note(action, claim.branch, push))
        self._log_handed_back(claim, action.reason, push)

    def release(self, claim: Claim, repo_dir: Path, reason: str) -> None:
        """Pushes what was left unpushed, then puts the ticket back in the queue for another slot to take.

        For a ticket that is fine but cannot start on this host, which is no question for a person about the ticket.
        """
        push = self._push_leftovers(claim, repo_dir)
        note = _release_note(reason, claim.branch, push)
        if push is not None and not push.ok:
            # The next claimant would start without the work stranded here, so a person decides instead.
            self._client.escalate(self._log.agent, note)
        else:
            try:
                self._client.release(self._log.agent, note)
            except ApiError as err:
                if not 400 <= err.status < 500:
                    raise
                # A refusal says the same on every retry; the ticket must not stay held by a slot that cannot run it.
                self._log.warning("release of ticket #%d refused, escalating it instead: %s", claim.ticket_id, self._log.clean(str(err)))
                self._client.escalate(self._log.agent, note)
        self._log_handed_back(claim, reason, push)

    def _push_leftovers(self, claim: Claim, repo_dir: Path) -> SafetyPush | None:
        if claim.has_paths and self._repo.has_unpushed_work(repo_dir, claim.branch):
            return self._repo.safety_push(repo_dir, claim.branch)
        return None

    def _log_handed_back(self, claim: Claim, reason: str, push: SafetyPush | None) -> None:
        safety = "no unpushed work" if push is None else ("safety push ok" if push.ok else "safety push FAILED")
        self._log.info("handed ticket #%d back: %s; %s", claim.ticket_id, self._log.clean(reason), safety)
        if push is not None and not push.ok:
            self._alerts.alert(f"{self._log.agent}: safety push of {claim.branch} failed: {push.error}")


def _escalation_note(action: Action, branch: str, push: SafetyPush | None) -> str:
    return _note(f"The supervisor gave up on this ticket: {action.reason}.", branch, push)


def _release_note(reason: str, branch: str, push: SafetyPush | None) -> str:
    return _note(f"The supervisor could not start a run on this host, so handed the ticket back for another: {reason}.", branch, push)


def _note(lead: str, branch: str, push: SafetyPush | None) -> str:
    parts = [lead]
    if push is not None:
        if push.ok:
            parts.append(f"Unpushed work was pushed to {push.rescue_ref or branch} as {push.commit}.")
        else:
            parts.append(f"Unpushed work could NOT be pushed ({push.error}); it is still in the slot.")
        if push.untracked:
            parts.append("Untracked files left behind: " + ", ".join(push.untracked) + ".")
    return " ".join(parts)
