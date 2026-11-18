# Running Codex

[Codex](https://github.com/openai/codex) (OpenAI) is the third harness a worker can use, after Claude Code and pi. It runs workers and a Remote Control session. Its steward is conditional: it only runs when a safety check passes (see [The steward](#the-steward)).

## Logging in

Codex uses a **ChatGPT login only**. There is no API-key setting: `OPENAI_API_KEY` and `CODEX_API_KEY` in `secrets.env` or in the supervisor's environment are removed from every Codex run, so a stray key cannot override the login.

On the machine, as the user that runs the supervisor:

```
CODEX_HOME=<root>/harness/codex codex login
```

`<root>` is the workspace folder (the one holding `secrets.env`). If the login is missing, the supervisor refuses to start and prints this command. After fixing it, `slop-haus slot unpause <slot>` if a worker was paused for it.

A Remote Control session the supervisor runs uses this same login (`<root>/harness/codex`). One started by hand has its own, in `<state>/codex-home` (under the Remote Control's state folder); the host prints the exact `codex login` command when it starts.

## Choosing Codex

Make the worker's harness `codex` when you set the workspace up (`slop-haus init --harness codex`). Its model and effort are `model=…,effort=…`. `effort` is one of `low`, `medium`, `high`, `xhigh`, `max` or `ultra`; which of them a model accepts varies, and Codex rejects one the model does not support, so the run fails as a setting problem and is not retried. `provider`, `thinking` and `subagent_model` are not Codex settings.

## One login, one worker

All the workers of one workspace share one Codex login. ChatGPT refresh tokens rotate, so several Codex processes refreshing at once can log every worker out. With the flag below the supervisor does the refreshing itself, under a lock, and gives each worker its own copy of the login; that has not yet been proven against a real login. Until it is, the supervisor refuses to start with more than one Codex worker. Once you have decided the risk is acceptable, add this to `run.env` and restart the supervisor:

```
SLOP_HAUS_CODEX_SHARED_LOGIN=verified
```

## Things to know

- **Codex never asks before running a tool**, like pi: workers run with the permissions of the user that runs the supervisor. Run them on a machine or container you can afford to lose.
- **Cost.** The board shows token counts only; Codex reports no cost.
- **Instructions.** Codex reads `AGENTS.md`; the supervisor also asks it to fall back to `CLAUDE.md`. That fallback has not been verified against a real model, so keep a repo's contract in `AGENTS.md` too.
- **Version.** Codex's protocol is experimental, so slop-haus is tested against one version to Codex **0.165.0**. Install that version; a newer one may break.

## The steward

The steward reads untrusted text, so a Codex steward runs in a locked-down shell. It can read the checkouts it reviews and the system tools it needs (`git`, `cat`, `rg`), and nothing else: not your home folder, `secrets.env`, other workers or the Codex login. It cannot write anywhere or reach the network, and its commands get no secrets in their environment.

Before it first runs, the supervisor tests those limits. If any fails, the Codex steward does not start and the supervisor says which limit did not hold (for example "the shell can open network connections", or "… is readable from the steward's shell"). This is deliberate: a steward that can write or read secrets is worse than none.

If it refuses:

- Read the message: it names the check. A refusal that says the sandbox "could not be asked" means Codex's sandbox does not work on this machine (Codex missing, or user namespaces disabled), so fix that first.
- Do not work around the check. Run the steward with Claude Code until the machine's sandbox holds.

To use it, open the Workers page, find the steward's row and set its Harness config to Codex. The change applies at the steward's next run. If the check fails, no steward runs (it does not switch to Claude Code) and the steward's row and Tenant Settings → Steward say which limit did not hold. Fix the machine or set the steward back to Claude Code; the check runs again at the next run.
