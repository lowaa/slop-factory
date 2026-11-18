# Running the supervisor as a service

On Linux (systemd) and macOS (launchd) run `slop-haus service install`. It checks the host's tools
against the PATH the service will get, writes the unit or plist for the tenant with the absolute path of
`uv` and the same `uv run` arguments `slop-haus start` uses, hands a supervisor already running
(from `slop-haus start` or a screen) over to it, starts it, and follows the log until the slots are up
or the supervisor exits, showing why. It is safe to re-run: an unchanged unit is left alone and a changed one is shown as a
diff. `slop-haus service uninstall` stops and removes it. Lingering needs root, so the command prints
the `sudo loginctl enable-linger <user>` line when it is off rather than running it.

The files below are references for other launchers, or for doing by hand what the command does.
The supervisor drains its running work before it exits, so a process manager's stop timeout must exceed the
drain timeout (default 120s) by the scheduler's join grace and the kill grace.

`slop-haus-supervisor run --tenant <tenant>` is a plain foreground process that behaves the
same under any launcher. Whatever starts it, the command is
`uv run --frozen --no-dev --project <root>/app slop-haus-supervisor run --tenant <tenant>`. The
supervisor reads `<root>/secrets.env` and `<root>/run.env` itself, so a launcher sets no
environment (`run.env.example` shows what goes in the second). The one thing a launcher must
find itself is `uv`, because it is looked up before `run.env` is read: both samples use an
absolute path to it, to edit.

A service starts with the manager's PATH, not your shell's (`~/.local/bin`, nvm and so on are missing). Anything
the workers need that is not on it goes in the base `run.env`'s `PATH=` line, as absolute paths.

## Linux: systemd user unit

```
mkdir -p ~/.config/systemd/user
cp slop-haus-supervisor@.service ~/.config/systemd/user/
systemctl --user daemon-reload
systemctl --user enable --now slop-haus-supervisor@<tenant>
journalctl --user -u slop-haus-supervisor@<tenant> -f
```

A user unit stops when its owner logs out unless lingering is on. That is a one-time step
that needs root, and nothing here runs it: `sudo loginctl enable-linger <user>`.

## macOS: launchd agent

Copy `com.slop-haus.supervisor.plist` to
`~/Library/LaunchAgents/com.slop-haus.supervisor.<tenant>.plist`, replace `TENANT` and
`USER` in it, then:

```
plutil -lint ~/Library/LaunchAgents/com.slop-haus.supervisor.<tenant>.plist
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.slop-haus.supervisor.<tenant>.plist
launchctl bootout gui/$(id -u)/com.slop-haus.supervisor.<tenant>    # stop
```

The log is `~/Library/Logs/slop-haus-supervisor-<tenant>.log` (rotated by the supervisor; the explicit `--log-file` replaces the default `<root>/supervisor.log`).
Put the keys git needs in the keychain (`ssh-add --apple-use-keychain`, `UseKeychain yes` in
`~/.ssh/config`): an agent started from a terminal is not there for a launchd job.

## Windows: WSL2

Native Windows is not supported (the supervisor needs `fcntl`, process groups and bash).
Run everything above inside a WSL2 distro, and start it either way:

- **systemd in WSL:** set `[boot]` `systemd=true` in `/etc/wsl.conf` inside the distro,
  `wsl --shutdown`, and use the Linux unit. WSL starts the distro, and so the unit, when
  something first opens it.
- **Task Scheduler:** a task "at log on" running
  `wsl.exe -d <distro> -- bash -lc "cd ~ && uv run --frozen --no-dev --project ~/slop-haus/<tenant>/app slop-haus-supervisor run --tenant <tenant>"`.
  Ending the task ends `wsl.exe` and so the command, without a drain; to stop it cleanly run
  `wsl.exe -d <distro> -- pkill -TERM -f "slop-haus-supervisor run"` first.

WSL shuts a distro down a few seconds after its last process exits and on `wsl --shutdown`
(and the Windows host restarting), and the supervisor goes with it: its runs are killed
and resumed by the next start. With systemd enabled the running unit keeps the distro up; with
Task Scheduler the task's `wsl.exe` does, for as long as it runs.

## Anywhere: tmux or a terminal

```
tmux new -s slop-haus 'uv run --frozen --no-dev --project ~/slop-haus/<tenant>/app slop-haus-supervisor run --tenant <tenant>'
```

Ctrl-C drains; a second Ctrl-C stops at once. Nothing restarts it.
