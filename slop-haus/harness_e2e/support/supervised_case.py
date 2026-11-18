"""What a scenario about a run the supervisor starts has in common: a one-slot supervisor, set up
by the board's own setup script, that claims a ticket and runs the real `claude -p` on it against
the scripted model. This is how a worker is started now, so a scenario about what a worker's
harness does (its MCP connection, what it reports, what it is offered) starts it this way, not
through a launcher of the test's own."""

from __future__ import annotations

from harnesses.codex import Codex
from harnesses.pi import Pi
from support.harness_case import MODEL, HarnessCase
from support.supervisor_case import RunningSupervisor, SupervisorRoot, add_slot, point_at, set_up_root, use_codex, use_pi

# The model's login lives in secrets.env, which only the supervisor reads (a dummy here).
SECRETS = {
    "claude": "ANTHROPIC_API_KEY=sk-ant-scripted-model-not-a-real-key\n",
    "pi": "".join(f"{k}={v}\n" for k, v in Pi.login_env("").items()),
    "codex": "".join(f"{k}={v}\n" for k, v in Codex.login_env("").items()),
}


class SupervisedCase(HarnessCase):
    # A scenario that is about Claude Code's own behaviour says so, and pi and Codex skip it.
    runs_on_pi = False
    runs_on_codex = False

    def setUp(self) -> None:
        if (self.stack.harness == "pi" and not self.runs_on_pi) or (self.stack.harness == "codex" and not self.runs_on_codex):
            self.skipTest("this scenario is about a Claude Code slot")
        super().setUp()

    def supervise(self, name: str, branches: tuple[str, ...], server: str | None = None, nested_repo: str | None = None) -> SupervisorRoot:
        """A root with one signed-in slot, whose checkout's origin holds the attempt `branches`. The
        supervisor's calls to the board go to `server` when given (a fault proxy), else straight.
        With `nested_repo`, the slot is a nested workspace: its directory is not a repo, that folder in it is."""
        sup = set_up_root(self.board, self.tmp / "tenant", self.tmp / "supervisor-home")
        add_slot(self.board, sup, name, branches, nested_repo)
        if server:
            point_at(sup, server)
        sup.secrets.write_text(SECRETS[self.adapter.name])
        harness_dir = sup.root / "harness" / self.adapter.name
        self.adapter.prepare_remote_control(harness_dir, sup.slot_dir, self.stack.model_server)
        if nested_repo:
            self.adapter.prepare_remote_control(harness_dir, sup.slot_dir / nested_repo, self.stack.model_server)
        if self.adapter.name == "pi":
            use_pi(sup, self.adapter.harness_config(MODEL))
        if self.adapter.name == "codex":
            use_codex(sup, self.adapter.harness_config(MODEL), server or self.stack.server)
        return sup

    def run_supervisor(self, sup: SupervisorRoot, log_name: str = "supervisor.log", env: dict[str, str] | None = None) -> RunningSupervisor:
        running = RunningSupervisor(
            sup, self.logs / log_name, {"ANTHROPIC_BASE_URL": self.stack.model_server, **self.adapter.hardening_env(), **(env or {})}
        )
        self.addCleanup(running.close)
        return running
