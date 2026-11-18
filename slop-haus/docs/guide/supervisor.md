# Running workers on your machine

Workers do the tickets. They run on a machine you own, started and watched by a program called the **supervisor**. This guide sets one up, keeps it running and fixes the usual problems.

## What a supervisor and its workers are

- The **supervisor** is a small program that runs in the background on your machine. It asks slop-haus which tickets are ready, starts a worker on each, and reports what is happening so the board can show it.
- A **worker** (the board also calls it a slot or an agent) is one checkout of your repos on that machine, with its own sign-in. The supervisor runs one worker per ticket at a time, so three workers work three tickets in parallel.
- Everything a worker does happens on your machine, with your git access and your harness login. slop-haus never sees your secrets.

One supervisor serves one workspace. If you use several workspaces on a machine, each has its own supervisor, workers and settings.

## Requirements

- Linux, macOS, or Windows through WSL2.
- `uv`, to run the supervisor.
- The harness your workspace uses: **claude** (Claude Code), **pi** or **codex** ([Running Codex](codex.md)).
- git that can push **without a prompt**, over SSH (a key with no passphrase, or an ssh-agent) or over HTTPS (a stored credential helper). Workers cannot answer a password question.
- docker, if your repos' tests use it, and about 30 GB of free disk. Setup warns below that.

## Install and start

1. At the bottom of the **Workers** page, open **Add a machine**, download the setup script and run it on the machine:

   ```
   bash slop-haus-supervisor-setup.sh
   ```

   It installs the `slop-haus` command (in `~/.local/bin`; it tells you if that needs adding to your `PATH`).

2. Set the supervisor up. The script does this for you when it has a terminal; it is safe to run again and picks up where it stopped:

   ```
   slop-haus setup
   ```

   Setup checks the machine, signs the supervisor in to slop-haus, logs the worker harness in (Claude Code: it asks for the token `claude setup-token` makes; pi and Codex: it runs their own login for you, see `slop-haus harness login` and [Running Codex](codex.md)), and adds workers (unless your workspace provisions from the board, in which case it tells you to add them there, see below).

   `slop-haus harness login NAME` logs a harness in on this host at any time, and `slop-haus harness status` shows which harnesses are logged in. pi and Codex keep their login in `<root>/harness/pi` and `<root>/harness/codex`; run it as the user that runs the supervisor.

3. Start it:

   ```
   slop-haus start
   ```

Day to day:

```
slop-haus status     # is it running, what each worker is doing, the last error
slop-haus logs       # follow the log
slop-haus stop       # finishes what is safe to finish, then stops
slop-haus update     # installs a new release
```

`slop-haus update` only swaps the files. Run `slop-haus stop && slop-haus start` afterwards, or the old version keeps running. The supervisor's group on the Workers page shows a notice when it needs this.

`slop-haus stop` lets each worker reach a safe pause (waiting for CI, say) so its work resumes later. `slop-haus stop --now` ends them immediately.

## Running it as a service

`slop-haus start` is enough for a machine you log in to. Run it from a login shell or over SSH: inside an agent session it refuses (unless you pass `--allow-host-session`), because the supervisor would die when that session ends. Use a service manager when the supervisor should come back after a reboot, or restart itself after an update.

On Linux and macOS, run `slop-haus service install`. It writes the systemd user unit or the launchd agent, drains a supervisor you already started with `slop-haus start` (asking first if a slot is mid-ticket; `--when-idle` waits for it, `--yes` doesn't ask), starts the service and follows the log until the slots are up, or shows why the supervisor exited. Re-run it any time; `slop-haus service uninstall` removes it.

A service starts with the manager's PATH, not your login shell's, so a tool that lives in `~/.local/bin` or under nvm is invisible to it. `slop-haus setup` and `service install` check the tools against the service's PATH and offer to add the directory to `run.env`'s `PATH=` line. Lingering (the unit surviving your logout) needs root: when it is off, `service install` prints the `sudo loginctl enable-linger <you>` line for you to run.

The supervisor is an ordinary foreground process, so any other manager can run it: `slop-haus run`. Sample files and a README for each platform ship with the supervisor, in its `service/` folder.

- **Linux (systemd user unit).** `slop-haus service install`.
- **macOS (launchd).** `slop-haus service install`. The supervisor's log is `<workspace>/supervisor.log`, as for `slop-haus start`.
- **Windows.** Use WSL2: run the Linux setup inside a distro and start it with systemd enabled in `/etc/wsl.conf`, or from a Task Scheduler task at logon running `wsl.exe -d <distro> -- slop-haus run`. Shutting WSL down ends the running workers; their tickets resume at the next start. Native Windows is not supported.
- **Anywhere.** tmux or a terminal also works.

Tell the manager to **restart on failure** but not when the supervisor exits normally. The exit codes:

| Code | Meaning | Restart? |
|---|---|---|
| 0 | Stopped on request | No |
| 1 | Failed | Yes |
| 2 | Wrong command line | No |
| 3 | The server wants a newer supervisor | Yes |
| 75 | Another supervisor already runs for this workspace | No |
| 78 | Configuration problem (see [When something is wrong](#when-something-is-wrong)) | No |

Give the manager a stop timeout of about 150 seconds, so workers have time to pause.

## Secrets and environment

Workers get their secrets from your machine only. Nothing about them is stored in slop-haus.

- **Secrets** live in `<root>/secrets.env`. Add one with:

  ```
  slop-haus secret set NAME
  ```

  It asks for the value at a hidden prompt (or reads it from standard input: `pass show x | slop-haus secret set NAME`). It never takes the value as an argument, which would end up in your shell history. `slop-haus secret list` prints the names, and `slop-haus secret unset NAME` removes one.

  Every secret in this file is in the environment of **every worker run**. To give one agent its own value or an extra secret, use `slop-haus secret set NAME --agent AGENT`: it is layered over this file for that agent's runs only, and takes effect on its next run. Claude Code's login (`CLAUDE_CODE_OAUTH_TOKEN` or `ANTHROPIC_API_KEY`) is stored here too; pi and Codex keep theirs under `<root>/harness/`. Values are hidden as `***` in logs and failure messages, but a worker can read its own environment, so only put in what you are happy for workers to use.

  Restart the supervisor after changing this file; a per-agent change needs no restart.

- **`run.env`** (in the same folder) holds non-secret settings every worker inherits: extra `PATH` entries for tools such as uv, go or node (absolute paths only; `~` and `$HOME` are not expanded), `DOCKER_HOST`, and so on. Neither file is run as a script: a `$(...)` in them is just text.

- **Labels.** Put `SLOP_HAUS_LABELS` in `run.env` to describe this machine's workers, as comma-separated items such as `gpu=a100,docker`. They are reported with each worker's sessions so you can tell machines apart. Restart the supervisor to pick up a change.

- **Your repos' own `.env`.** A fresh checkout lacks files that are not in git. Put a file at `<root>/repo.env` and each worker gets a copy as `.env` in its checkout.

- **Reviews.** When a worker asks for a review of its pushed change, the supervisor runs slop-review on it itself, so the worker cannot change the result. Put `SLOP_HAUS_REVIEW_SCRIPT=/abs/path/to/slop-factory/slop-review/review.sh` in `run.env`, naming `review.sh` in a slop-factory checkout on this machine (slop-review needs `go` and its provider's login, as when you run it yourself). The review is one slop-review pass (`--persona=standard`, with slop-review's single-pass defaults: the whole change in one session, 40 turns, `xhigh` effort where the provider takes one). It runs in a fresh clone under `<root>/state/reviews/`, with the worker's `repo.env` as its `.env`, and posts its run with the supervisor's own sign-in (`slop-haus login`). Posting that run closes the worker's request, and the steward approves the change only if it approves, covered the whole change and raised no blocker or major finding. The supervisor stops a review that passes 90 minutes. Unset, a worker that asks is told this machine cannot review. Restart the supervisor to pick up a change.

  The review uses the supervisor's own logins, never the ones in your home directory (`~/.claude`, `~/.codex`, pi's `~/.pi/agent`), whichever provider it runs: Claude Code's from `<root>/harness/claude`, Codex's and pi's from `<root>/harness/codex` and `<root>/harness/pi`, and API keys and Claude Code's token from `secrets.env` and the worker's layers (`repo.env` too), not from the supervisor's own environment. The provider is `SLOP_REVIEW_PROVIDER` from the worker's `run.env`, `secrets.env` or `repo.env`, `claude` if it is not set. Before each review the supervisor checks that provider's login. If it is missing, nothing runs, and the worker is told what is missing and the command to fix it:

  | Provider | Needs | Fix |
  |---|---|---|
  | `claude` | `CLAUDE_CODE_OAUTH_TOKEN` or `ANTHROPIC_API_KEY` | `slop-haus harness login claude`, or `slop-haus secret set ANTHROPIC_API_KEY` |
  | `codex` | a ChatGPT login in `<root>/harness/codex`, and `SLOP_HAUS_CODEX_SHARED_LOGIN=verified` in `run.env` | `slop-haus harness login codex` |
  | `chatgpt` | pi's ChatGPT login in `<root>/harness/pi` | `slop-haus harness login pi`, then `/login` and Sign in with ChatGPT |
  | `openrouter`, `openai` | `OPENROUTER_API_KEY` or `OPENAI_API_KEY` | `slop-haus secret set NAME` (add `--agent AGENT` for one worker) |

  A Codex review runs on its own copy of the login, as each Codex worker does, so it never uses the stored refresh token itself. Only the supervisor's one refresher, which needs the shared-login setting, keeps that copy fresh. The copy is deleted when the review ends. If the slop-haus server's **Providers** page names a provider, slop-review uses that one, and its fallback, instead of `SLOP_REVIEW_PROVIDER`. The supervisor can't check those in advance, so log them in here too: without a login they fail when the review runs, and never fall back to yours. `HOME` stays the user's, because slop-review's Claude sandbox uses it to block reads of the credential stores under it (`~/.ssh`, `~/.codex` and others).

## Adding and managing workers

**From the board**, which a new workspace has on from the start (an older workspace's owner turns it on): open **Workers** and find the machine's group: the Workers page lists workers under the machine that runs them. Choose **Add worker** in its header, pick a kind and a name; the row moves from *Waiting for supervisor* to *Provisioning* to *Running*, and shows the reason if it fails. Each worker's row on that page also pauses, resumes and retires it, and sets its harness. Workers no supervisor runs (signed in by hand) are listed last, under *Not on a supervisor*. A worker with unpushed work will not retire: push or discard the work, and the supervisor retires it on its next try. If there is no supervisor yet, add a machine first (above): on a workspace that provisions from the board, `slop-haus setup` signs the machine in and adds no workers itself, then tells you to open **Workers** to add them. A machine whose supervisor has not signed in (`slop-haus login`, which setup does) is not managed by the board.

**From the machine**, when board provisioning is off:

```
slop-haus slot add --count 2     # two more workers, named <host>-<n>
```

While board provisioning is **on**, the `slot add`, `slot remove` and `slot unpause` commands refuse and point at the board page instead, so there is one place that decides.

### The worker cap

A supervisor may run only so many workers at once (1 to 16). The number is chosen when the machine is signed in, and the machine's header on the **Workers** page shows it as *N of cap workers*. The workspace's owner can change it there with **Change cap**: the new cap applies at once, with no restart. Raising it lets the supervisor create the workers it was refused; they appear on its next reconcile, within about five minutes.

Lowering it below the number of workers retires none of them. They keep running, the row says the supervisor is over its cap, and it can create no more until enough workers retire to leave a place free.

If a worker shows *Failed* with `the supervisor is at its slot cap: retire a slot first`, the supervisor already has as many workers as its cap allows. Retire a worker you no longer need, or raise the cap.

A worker that exists on the machine but that the board does not manage shows as *unmanaged*. **Import** hands it to the supervisor without touching its files.

Each worker checks out your repos into its own folder. The supervisor picks SSH or HTTPS cloning once, from what already works on the machine, and keeps to it. If a clone fails, the message names the fix, since git never waits for a prompt.

## Choosing the model

The model and effort workers use are set per workspace. For a worker run by a supervisor, they come from the worker's default variant, which the supervisor reads from its configuration (`model=…,effort=…` for Claude Code; `provider=…,model=…,thinking=…` for pi). A change applies from the next ticket a worker starts. Running work keeps the settings it started with.

## Running pi

Each board-provisioned worker has its own harness: choose it from the dropdown on the worker's row on the Workers page. A change takes effect at the worker's next run, after the current one has finished. The model and effort of a run come from the worker's harness settings on the Workers page, for the harness it runs. Workers added another way, and hosts whose workspace is not provisioned from the board, use the `default_harness` in `supervisor.toml`.

[pi](https://pi.dev) is the other harness a worker can use. It **never asks before running a tool**: whatever the model decides, it runs. The only protection is the one you provide, so run pi workers on a machine or in a container that you can afford to lose.

Install pi on the machine, sign in to it (`pi` prompts on first use), and the supervisor runs it for you; nobody starts it by hand. Setup checks for `pi` instead of `claude` when your workspace uses it.

[Codex](codex.md) is a third harness, with a ChatGPT login rather than a token or key.

## When something is wrong

Start with `slop-haus status`, then `slop-haus logs`. The most common messages:

| You see | It means | What to do |
|---|---|---|
| `the supervisor is at its slot cap: retire a slot first` | The supervisor has as many workers as its cap allows, so the board's new workers are refused. | Raise the cap in the machine's header on **Workers** (workspace owner), or retire a worker. The refused workers are created on the next reconcile. |
| `supervisor has no grants left` | Its sign-in was revoked on the board, and its workers were disabled with it. | Run `slop-haus setup` to sign in again. If it names workers it could not restore, enable them on the board, then run `slop-haus slot reissue --all`. |
| *Not signed in* in `status` | The supervisor has no valid sign-in. | `slop-haus login` (or `setup`). |
| Exit 78, *git cannot push* | The pre-start check could not push without a prompt: a refused key, an unknown host key, or an HTTPS remote asking for a username. | Fix git access: a passphrase-free key, an ssh agent (put `SSH_AUTH_SOCK` in `run.env`), the macOS keychain, or an HTTPS credential helper. Check by running `git push --dry-run` by hand in a checkout. |
| Exit 78, *no credential* or *no slots* | Setup has not finished. | Run `slop-haus setup`. |
| Exit 75, *another run holds this workspace* | A supervisor is already running for it. | `slop-haus status`; stop the other one first. |
| *Supervisor protocol is stale* banner on the board | The server is newer than the running supervisor. | `slop-haus update && slop-haus stop && slop-haus start`. |
| A clone fails and names a repo | The machine cannot reach that repo without a prompt. | Fix access as for the git push error above, then `slop-haus slot rebuild`. |
| A worker shows *Failed* when retiring | It has unpushed work. | Push or discard that work in its folder; the supervisor retries. The board drops the row 10 minutes after the slot is archived, whatever the supervisor reports. |
| *No machine runs workers yet* on the Workers page | No supervisor has reported in. | Run `slop-haus start`, and `slop-haus status` to see why it is not up. |
| The board says a supervisor is *offline* | It has not reported for a while: the machine is off or asleep, or the supervisor stopped. | `slop-haus status` on the machine, then `slop-haus start`. |
| A worker stops with a usage-limit message | The harness account has run out of allowance. | Wait for the allowance to reset; the ticket resumes afterwards. |

If you did not find it here, the last lines of `slop-haus logs` usually name the cause.
