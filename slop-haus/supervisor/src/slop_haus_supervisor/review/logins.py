"""Which provider login a supervisor's review runs under: always the supervisor's scoped one, never the host user's.

slop-review's providers find their logins by environment variable, each falling back to the home directory when it is
unset: Claude Code to ~/.claude, Codex to ~/.codex, pi (the chatgpt provider) to ~/.pi/agent. So every review gets all
three pointed under the root, whichever provider it names, and one whose provider has no login there is not started.
"""

from __future__ import annotations

from collections.abc import Mapping
from dataclasses import dataclass
from pathlib import Path

from slop_haus_supervisor.harness.codex import API_KEY_ENV as CODEX_API_KEYS
from slop_haus_supervisor.harness.codex_login import CodexLogin
from slop_haus_supervisor.service.run_env import CODEX_SHARED_LOGIN
from slop_haus_supervisor.usage.billing import pi_oauth_login, read_codex_login

PROVIDER_ENV = "SLOP_REVIEW_PROVIDER"
DEFAULT_PROVIDER = "claude"
# The pi provider slop-review's chatgpt provider asks for a token (internal/piauth).
PI_CHATGPT_PROVIDER = "openai"
# The variables that point a provider CLI at its login. Always set here, so a layer or the host cannot point them home.
LOGIN_DIRS = ("CLAUDE_CONFIG_DIR", "CODEX_HOME", "PI_CODING_AGENT_DIR")
# Credentials a provider reads from the environment. The host's own are dropped: a review has the agent's layered ones only.
CREDENTIAL_NAMES = (
    "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "OPENAI_API_KEY", "CODEX_API_KEY", "OPENROUTER_API_KEY",
)
# The API-key providers, and the key each is paid with.
_API_KEYS = {"openrouter": "OPENROUTER_API_KEY", "openai": "OPENAI_API_KEY"}
PROVIDERS = ("claude", "codex", "chatgpt", *_API_KEYS)
CLAUDE_LOGINS = ("CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY")
CODEX_COPY = "codex-home"


def review_provider(env: Mapping[str, str]) -> str:
    """The provider slop-review picks from this environment when nothing on its command line overrides it."""
    return env.get(PROVIDER_ENV, "").strip() or DEFAULT_PROVIDER


@dataclass(frozen=True)
class ReviewLogins:
    """The supervisor's logins under its root, and the Codex login's refresher when slots share it."""

    claude_dir: Path
    codex_dir: Path
    pi_dir: Path
    # None unless SLOP_HAUS_CODEX_SHARED_LOGIN=verified: without its lock, a review is one more unsynchronised user of the login.
    codex_login: CodexLogin | None = None

    @classmethod
    def under(cls, root: Path, codex_login: CodexLogin | None = None) -> ReviewLogins:
        harness = root / "harness"
        return cls(harness / "claude", harness / "codex", harness / "pi", codex_login)

    def missing(self, env: Mapping[str, str], agent: str) -> str:
        """Why a review of `agent`'s change cannot run under its provider's scoped login, or "" when it can. Names, never values."""
        provider = review_provider(env)
        if provider == "claude":
            if any(env.get(name) for name in CLAUDE_LOGINS):
                return ""
            return (
                f"the claude provider needs {' or '.join(CLAUDE_LOGINS)} in the supervisor's secrets: on its host run "
                f"`slop-haus harness login claude`, or `slop-haus secret set ANTHROPIC_API_KEY` (`--agent {agent}` for this agent only)"
            )
        if provider == "codex":
            login = read_codex_login(self.codex_dir)
            if not (login.auth_mode == "chatgpt" and login.has_tokens):
                return f"the codex provider needs a ChatGPT login in {self.codex_dir}: on the supervisor's host run `slop-haus harness login codex`"
            if self.codex_login is None:
                return (
                    f"the codex provider reviews on its own copy of the supervisor's Codex login, which only the shared login's "
                    f"refresher keeps fresh: set {CODEX_SHARED_LOGIN}=verified in run.env and restart the supervisor"
                )
            return ""
        if provider == "chatgpt":
            if pi_oauth_login(self.pi_dir, PI_CHATGPT_PROVIDER):
                return ""
            return (
                f"the chatgpt provider needs pi's ChatGPT login in {self.pi_dir}: on the supervisor's host run "
                "`slop-haus harness login pi`, then /login and Sign in with ChatGPT"
            )
        key = _API_KEYS.get(provider)
        if key is None:
            return f"{PROVIDER_ENV} names {provider[:60]!r}, which is not a provider the supervisor can check a login for ({', '.join(PROVIDERS)})"
        if env.get(key):
            return ""
        return f"the {provider} provider needs {key}: on the supervisor's host run `slop-haus secret set {key}` (`--agent {agent}` for this agent only)"

    def scoped(self, env: Mapping[str, str], review_dir: Path) -> dict[str, str]:
        """`env` with every provider's login under the root. Codex's is a copy in `review_dir`, refreshed first when it is the one used.

        A copy, as each Codex slot has, so the review's Codex never presents the master's refresh token: providers rotate
        it and revoke the whole family on a second use, which would log every slot out. One refresh at the start covers the
        review, as one at launch covers a slot's run: the refresher keeps a login days short of Codex's staleness, and a
        review ends within its budget.
        """
        out = dict(env)
        codex_home = review_dir / CODEX_COPY
        if review_provider(env) == "codex":
            # As in a Codex slot: a key in the environment would override the ChatGPT login.
            for key in CODEX_API_KEYS:
                out.pop(key, None)
            if self.codex_login is not None:
                self.codex_login.refresh_if_due([codex_home])
        out["CLAUDE_CONFIG_DIR"] = str(self.claude_dir)
        # Set whichever provider runs: the server's provider config can pick another than the environment names, and that
        # one must find no login rather than the host user's.
        out["CODEX_HOME"] = str(codex_home)
        out["PI_CODING_AGENT_DIR"] = str(self.pi_dir)
        return out
