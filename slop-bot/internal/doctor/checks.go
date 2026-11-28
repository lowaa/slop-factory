package doctor

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/lowaa/slop-factory/slop-bot/internal/config"
	"github.com/lowaa/slop-factory/slop-bot/internal/paths"
	"github.com/lowaa/slop-factory/slop-bot/internal/service"
	"github.com/lowaa/slop-factory/slop-bot/internal/worktree"
)

func (d *Doctor) checkConfig() {
	const g = "config"
	o := d.opts
	switch {
	case o.ConfigErr != nil:
		fix := "fix " + o.ConfigFile + "; the harness and repo checks are skipped until it loads"
		if o.ConfigFile == "" {
			fix = "fix the runtime dir (below); the config, harness and repo checks wait on it"
		}
		d.add(g, "config file", Fail, o.ConfigErr.Error(), fix)
		return
	case o.ConfigMissing && o.Installing:
		d.add(g, "config file", Pass, o.ConfigFile+" (install writes it)", "")
	case o.ConfigMissing:
		d.add(g, "config file", Warn, "none at "+o.ConfigFile+": running on the defaults",
			"slop-bot install writes a commented default there")
	default:
		d.add(g, "config file", Pass, o.ConfigFile, "")
	}
	d.checkBriefing(o.Config.BriefingPath)
}

// checkBriefing fails a briefing that is set but unreadable: the daemon
// refuses to start agents without it, rather than let them run without the
// user's workflow.
func (d *Doctor) checkBriefing(path string) {
	const g, name = "config", "briefing"
	switch {
	case path == "":
		d.add(g, name, Pass, "none (briefing_path is empty): agents get only slop-bot's own", "")
		return
	case d.opts.Installing && d.opts.Paths != nil && path == d.opts.Paths.DefaultBriefing():
		if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
			d.add(g, name, Pass, path+" (install writes a stub)", "")
			return
		}
	}
	f, err := os.Open(path) // #nosec G304 -- the user's own briefing_path
	if err == nil {
		var info os.FileInfo
		if info, err = f.Stat(); err == nil && info.IsDir() {
			err = errors.New("is a directory")
		}
		_ = f.Close()
	}
	if err != nil {
		d.add(g, name, Fail, describe(path, err),
			"write the briefing there, or set briefing_path to another file (empty for none)")
		return
	}
	d.add(g, name, Pass, path, "")
}

func (d *Doctor) checkPrerequisites(ctx context.Context) {
	const g = "prerequisites"
	d.checkGit(ctx)
	cfg := d.config()
	if cfg == nil {
		return
	}
	d.checkTmux(ctx, cfg)
	for _, bin := range cfg.Doctor.Require {
		path, err := d.opts.Commands.LookPath(bin)
		if err != nil {
			d.add(g, bin, Fail, "not on PATH (doctor.require lists it)",
				d.forOS(hint{linux: "install " + bin + ", e.g. sudo apt install " + bin + ", or drop it from doctor.require",
					darwin: "install " + bin + ", e.g. brew install " + bin + ", or drop it from doctor.require"}))
			continue
		}
		d.add(g, bin, Pass, path+" (doctor.require)", "")
	}
}

func (d *Doctor) checkGit(ctx context.Context) {
	const g, name = "prerequisites", "git"
	path, err := d.opts.Commands.LookPath("git")
	if err != nil {
		d.add(g, name, Fail, "not on PATH: slop-bot runs every agent in a git worktree", d.forOS(pkg("git")))
		return
	}
	v, ok := d.version(ctx, path, "--version")
	switch {
	case !ok:
		d.add(g, name, Fail, path+": couldn't read its version", d.forOS(pkg("git")))
	case compareVersions(v, minGit) < 0:
		d.add(g, name, Fail, fmt.Sprintf("%s is too old: worktree support needs %s or later", v, minGit),
			d.forOS(hint{linux: "upgrade git: sudo apt install git (on an old distribution, the git-core PPA)",
				darwin: "brew install git, and put Homebrew's bin before /usr/bin on PATH"}))
	default:
		d.add(g, name, Pass, fmt.Sprintf("%s, with worktree support (%s or later)", v, minGit), "")
	}
}

// checkTmux requires tmux only when a harness runs under the tui driver:
// programmatic sessions are child processes, and attach uses the user's
// own terminal.
func (d *Doctor) checkTmux(ctx context.Context, cfg *config.Config) {
	const g, name = "prerequisites", "tmux"
	var tui []string
	for n, h := range cfg.Harnesses {
		// A tui harness this build can't run needs no tmux; doctor says
		// it isn't implemented instead.
		if h.Driver == "tui" && d.implemented(n, h.Driver) {
			tui = append(tui, n)
		}
	}
	if len(tui) == 0 {
		d.add(g, name, Skip, "not needed: no harness this build runs uses the tui driver", "")
		return
	}
	path, err := d.opts.Commands.LookPath(name)
	if err != nil {
		d.add(g, name, Fail, "not on PATH: the tui driver ("+strings.Join(sorted(tui), ", ")+") needs it",
			d.forOS(pkg(name)))
		return
	}
	d.checkVersion(ctx, &versionCheck{group: g, row: name, key: name, name: name, path: path, args: []string{"-V"}})
}

// versionCheck is one tool's version against the tested table.
type versionCheck struct {
	group, row string
	// key is the tested table's entry; name is how the report names the tool.
	key, name string
	path      string
	args      []string
}

// checkVersion warns outside the tested range; it never fails, because a
// newer release usually works and the user may know it does.
func (d *Doctor) checkVersion(ctx context.Context, c *versionCheck) {
	v, ok := d.version(ctx, c.path, c.args...)
	r := tested[c.key]
	switch {
	case !ok:
		d.add(c.group, c.row, Warn, fmt.Sprintf("couldn't read %s's version (`%s %s`)", c.name, filepath.Base(c.path),
			strings.Join(c.args, " ")), "check "+c.path+" runs")
	case !r.contains(v):
		d.add(c.group, c.row, Warn, fmt.Sprintf("%s %s is outside the tested range (%s)", c.name, v, r),
			"it may work; if it misbehaves, install a version in the tested range")
	default:
		d.add(c.group, c.row, Pass, fmt.Sprintf("%s %s (tested %s)", c.name, v, r), "")
	}
}

func (d *Doctor) version(ctx context.Context, path string, args ...string) (string, bool) {
	res, err := d.run(ctx, path, args...)
	if err != nil || res.ExitCode != 0 {
		return "", false
	}
	return parseVersion(res.Stdout + res.Stderr)
}

func (d *Doctor) checkRepos(ctx context.Context, cfg *config.Config) {
	const g = "repos"
	if len(cfg.Repos) == 0 {
		d.add(g, "repos", Warn, "none configured: agents have no repo to make worktrees from",
			"add your repos under repos: in "+d.opts.ConfigFile+" (or `slop-bot install --repo name=path` on a first install)")
	}
	git, gitErr := d.opts.Commands.LookPath("git")
	for _, r := range cfg.Repos {
		info, err := os.Stat(r.Path)
		switch {
		case err != nil:
			d.add(g, r.Name, Fail, describe(r.Path, err), "correct repos."+r.Name+".path, or clone it there")
			continue
		case !info.IsDir():
			d.add(g, r.Name, Fail, r.Path+" isn't a directory", "point repos."+r.Name+".path at the repo's checkout")
			continue
		case gitErr != nil:
			d.add(g, r.Name, Skip, r.Path+": git isn't on PATH to check it", "")
			continue
		}
		// The worktree manager's own check: `rev-parse --git-dir` passes
		// anywhere inside a repo, and a subdirectory would pass here only to
		// park every ticket naming it as misconfigured.
		switch err := worktree.CheckRepo(ctx, &doctorGit{d: d, git: git}, r.Path); {
		case errors.Is(err, worktree.ErrNotTopLevel):
			d.add(g, r.Name, Fail, err.Error(), "point repos."+r.Name+".path at the repo's top level")
		case err != nil:
			d.add(g, r.Name, Fail, r.Path+" isn't a git repo: "+err.Error(), "point repos."+r.Name+".path at a git checkout")
		default:
			d.add(g, r.Name, Pass, r.Path, "")
		}
	}
	d.checkWorkspace(cfg.WorkspaceDir)
}

// checkWorkspace proves workspace_dir writable by writing to it, or to the
// nearest directory that exists when it doesn't yet: the daemon creates it.
func (d *Doctor) checkWorkspace(dir string) {
	const g, name = "repos", "workspace_dir"
	probe, created := dir, false
	for {
		info, err := os.Stat(probe)
		if err == nil {
			if !info.IsDir() {
				d.add(g, name, Fail, probe+" isn't a directory", "set workspace_dir to a directory")
				return
			}
			break
		}
		parent := filepath.Dir(probe)
		if !errors.Is(err, fs.ErrNotExist) || parent == probe {
			d.add(g, name, Fail, describe(dir, err), "set workspace_dir to a directory you can write")
			return
		}
		probe, created = parent, true
	}
	f, err := os.CreateTemp(probe, ".slop-bot-doctor-*")
	if err != nil {
		d.add(g, name, Fail, fmt.Sprintf("%s isn't writable: %v", probe, err), "set workspace_dir to a directory you can write")
		return
	}
	_ = f.Close()
	_ = os.Remove(f.Name())
	if created {
		d.add(g, name, Pass, dir+" (created on first use)", "")
		return
	}
	d.add(g, name, Pass, dir, "")
}

func (d *Doctor) checkPlatform(ctx context.Context) {
	const g = "platform"
	o := d.opts
	if o.PathsErr != nil {
		d.add(g, "runtime dir", Fail, o.PathsErr.Error(), d.forOS(hint{
			linux:  "log in through a session that sets XDG_RUNTIME_DIR (not `su`), or export XDG_RUNTIME_DIR=/run/user/$(id -u)",
			darwin: "export XDG_RUNTIME_DIR to a short directory you own, e.g. ~/.run, in your shell profile",
		}))
		// The service manager is built from the resolved paths.
		d.add(g, "service manager", Skip, "not checked until the paths resolve", "")
		return
	}
	d.add(g, "runtime dir", Pass, fmt.Sprintf("%s (%s)", o.Paths.Runtime.Path, source(o.Paths.Runtime)), "")
	d.add(g, "socket", Pass, fmt.Sprintf("%s (%d of %d bytes)", o.Paths.Socket(), len(o.Paths.Socket()),
		paths.MaxSocketPath), "")
	if o.Service == nil {
		d.add(g, "service manager", Fail, "slop-bot has no service manager for "+o.GOOS+" (Linux: systemd, macOS: launchd)",
			"run `slop-bot daemon` yourself, under whatever keeps it running")
		return
	}
	if err := o.Service.Preflight(ctx); err != nil {
		// The manager answered but disagrees with this shell; the message says how.
		fix := "align the two as the message says, then re-run"
		if errors.Is(err, service.ErrNoServiceManager) {
			fix = d.forOS(hint{
				linux: "log in through a normal session (ssh or a desktop, not `su`) so systemd starts your user manager; " +
					"on a headless box, also `loginctl enable-linger $USER`",
				darwin: "log in at the Mac: launchd runs per-user agents in your GUI session",
			})
		}
		d.add(g, "service manager", Fail, err.Error(), fix)
		return
	}
	d.add(g, "service manager", Pass, d.forOS(hint{linux: "the systemd user manager answers", darwin: "launchd answers"}), "")
	st, err := o.Service.Status(ctx)
	if err != nil {
		d.add(g, "service", Warn, err.Error(), "check `slop-bot service status`")
		return
	}
	d.checkLinger(st.Linger)
	d.checkService(st)
}

func (d *Doctor) checkLinger(l service.Linger) {
	const g, name = "platform", "linger"
	switch l {
	case service.LingerNotApplicable:
		return
	case service.LingerOn:
		d.add(g, name, Pass, "on: slop-bot keeps running with nobody logged in", "")
	case service.LingerOff:
		d.add(g, name, Warn, "off: systemd stops slop-bot when your last session ends",
			"to keep it running with nobody logged in (a headless box): loginctl enable-linger $USER")
	default:
		d.add(g, name, Warn, "unknown: loginctl couldn't say", "check `loginctl show-user $USER -p Linger`")
	}
}

// checkService reports the installed service, and any location it resolves
// differently from this shell: the CLI would then talk to a socket no
// daemon listens on.
func (d *Doctor) checkService(st *service.Status) {
	const g = "platform"
	o := d.opts
	if !st.Installed {
		if o.Installing {
			d.add(g, "service", Pass, "not installed yet (install writes "+st.UnitPath+")", "")
		} else {
			d.add(g, "service", Warn, "not installed", "slop-bot install")
		}
		return
	}
	running := "not running"
	if st.Running {
		running = "running"
	}
	d.add(g, "service", Pass, fmt.Sprintf("%s, %s (%s)", st.UnitPath, running, st.State), "")
	// Install rewrites the unit from this shell.
	if o.Installing || o.Paths == nil {
		return
	}
	d.checkServicePATH()
	svc, err := service.ServicePaths(o.Service)
	if err != nil {
		d.add(g, "service paths", Warn, err.Error(), "re-run slop-bot install")
		return
	}
	mm := service.Mismatches(o.Paths, svc)
	if len(mm) == 0 {
		d.add(g, "service paths", Pass, "the service and this shell agree on every location", "")
		return
	}
	for _, m := range mm {
		d.add(g, "service paths", Warn, fmt.Sprintf("%s: this shell resolves %s, the service %s", m.What, m.CLI, m.Service),
			MismatchFix(o.GOOS, m))
	}
}

// checkServicePATH warns when the service runs with another PATH than this
// shell's: the binaries doctor found here may be missing for the daemon
// and its agents.
func (d *Doctor) checkServicePATH() {
	env, err := d.opts.Service.ServiceEnv()
	if err != nil {
		return // reported with the service paths
	}
	shell, _ := d.opts.Lookup("PATH")
	svc, _ := env.Lookup("PATH")
	if svc == service.ServicePATH(shell) {
		d.add("platform", "service PATH", Pass, "the same as this shell's", "")
		return
	}
	d.add("platform", "service PATH", Warn, "the service runs with a different PATH from this shell's, so a tool "+
		"found here may be missing for agents", "re-run slop-bot install from this shell to give the service its PATH")
}

// MismatchFix is what to do when this shell and the service disagree on m.
func MismatchFix(goos string, m service.Mismatch) string {
	if goos == "linux" && m.What == "control socket" {
		// The unit doesn't carry XDG_RUNTIME_DIR, so re-installing can't
		// move the service's socket: the shell has to move.
		return "systemd always gives the service logind's runtime dir: export XDG_RUNTIME_DIR=" +
			filepath.Dir(filepath.Dir(m.Service)) + " in your shell, or unset it"
	}
	return "your shell's XDG_* variables differ from the service's: re-run `slop-bot install` from the shell " +
		"you use, or export the same values in your shell profile"
}

func source(d paths.Dir) string {
	switch d.Source {
	case paths.FromEnv:
		return "$" + d.Var
	case paths.FromFallback:
		return "fallback, $" + d.Var + " unset"
	default:
		return "default, $" + d.Var + " unset"
	}
}

func sorted(s []string) []string {
	out := slices.Clone(s)
	slices.Sort(out)
	return out
}

// describe is "<path>: <why>", without the path a *fs.PathError repeats.
func describe(path string, err error) string {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		err = pe.Err
	}
	return path + ": " + err.Error()
}

// doctorGit runs git through doctor's probes, for checks shared with the
// packages that run it for real.
type doctorGit struct {
	d   *Doctor
	git string
}

func (g *doctorGit) Run(ctx context.Context, dir string, args ...string) (string, error) {
	res, err := g.d.run(ctx, g.git, append([]string{"-C", dir}, args...)...)
	if err != nil {
		return "", err
	}
	if res.ExitCode != 0 {
		return "", fmt.Errorf("git %s exited %d: %s", strings.Join(args, " "), res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	return res.Stdout, nil
}
