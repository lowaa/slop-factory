package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/lowaa/slop-factory/slop-bot/internal/config"
	"github.com/lowaa/slop-factory/slop-bot/internal/control"
	"github.com/lowaa/slop-factory/slop-bot/internal/paths"
	"github.com/lowaa/slop-factory/slop-bot/internal/service"
	"github.com/lowaa/slop-factory/slop-bot/internal/setup"
)

// installFlags shape a first install; a re-run keeps the user's config.
type installFlags struct {
	binary    string
	repos     []string
	harnesses []string
}

func (a *app) installCmd() *cobra.Command {
	f := &installFlags{}
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Check this machine, write a default config and briefing, and install the service",
		Long: "Run doctor's checks, and stop before writing anything if a required one fails. " +
			"Then write a commented default config and a stub briefing where there are none " +
			"(a re-run keeps yours), install and (re)start the service, and print where " +
			"everything is.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return a.install(cmd, f) },
	}
	cmd.Flags().StringVar(&f.binary, "binary", "",
		"absolute path of the slop-bot binary the service runs (default: this one)")
	cmd.Flags().StringArrayVar(&f.repos, "repo", nil,
		"a repo for a new config, as name=path (repeatable)")
	cmd.Flags().StringSliceVar(&f.harnesses, "harness", nil,
		"harnesses for a new config, the first the default (default: claude-code)")
	return cmd
}

func (a *app) install(cmd *cobra.Command, f *installFlags) error {
	out := &errWriter{w: cmd.OutOrStdout()}
	p, perr := a.paths()
	lc := a.readConfig(p)
	var newConfig []byte
	switch {
	case lc.missing && p != nil:
		var err error
		if newConfig, err = a.defaultConfig(p, f); err != nil {
			return err
		}
		lc.cfg, lc.err = config.Parse(newConfig, filepath.Dir(lc.path), a.deps.Env.Home)
	case !lc.missing && len(f.repos)+len(f.harnesses) > 0:
		out.printf("note: keeping your config at %s; --repo and --harness only shape a new one, so edit it instead\n\n",
			lc.path)
	}
	report := a.newDoctor(cmd, p, perr, lc, true).Run(cmd.Context())
	if err := report.Write(out); err != nil {
		return err
	}
	if report.Failed() {
		return fmt.Errorf("install stopped before writing anything: %w", errChecksFailed)
	}
	sum := &installSummary{p: p, cfg: lc.cfg, configFile: lc.path, configFlag: a.configPath != ""}
	var err error
	if newConfig != nil {
		if sum.configWritten, err = setup.WriteNew(lc.path, newConfig); err != nil {
			return fmt.Errorf("writing %s: %w", lc.path, err)
		}
	}
	// Only the default location: a briefing the user pointed elsewhere is
	// theirs to write, and doctor has already said if it's missing.
	if lc.cfg.BriefingPath == p.DefaultBriefing() {
		if sum.briefingWritten, err = setup.WriteNew(lc.cfg.BriefingPath, []byte(setup.BriefingStub)); err != nil {
			return fmt.Errorf("writing %s: %w", lc.cfg.BriefingPath, err)
		}
	}
	m, err := a.installService(cmd, p, f.binary, sum)
	if err != nil {
		return err
	}
	// The socket the daemon really listens on: on Linux, logind's runtime
	// dir whatever this shell says.
	if svc, err := service.ServicePaths(m); err == nil {
		sum.serviceSocket = svc.Socket()
	}
	out.printf("\n")
	sum.write(out, a.deps.Env.GOOS)
	return out.err
}

func (a *app) defaultConfig(p *paths.Paths, f *installFlags) ([]byte, error) {
	repos := make([]config.Repo, 0, len(f.repos))
	for _, r := range f.repos {
		name, path, ok := strings.Cut(r, "=")
		if !ok || name == "" || path == "" {
			return nil, fmt.Errorf("--repo %q: want name=path", r)
		}
		if rest, ok := strings.CutPrefix(path, "~/"); ok {
			path = filepath.Join(a.deps.Env.Home, rest)
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			return nil, err
		}
		repos = append(repos, config.Repo{Name: name, Path: abs})
	}
	return setup.DefaultConfig(&setup.ConfigOptions{
		Harnesses: f.harnesses, Repos: repos, BriefingPath: p.DefaultBriefing(), Home: a.deps.Env.Home,
	})
}

// installService installs the service, recording what it wrote in sum.
func (a *app) installService(cmd *cobra.Command, p *paths.Paths, binary string, sum *installSummary) (service.Manager, error) {
	if binary == "" {
		var err error
		if binary, err = a.deps.FindBinary(); err != nil {
			return nil, fmt.Errorf("finding the slop-bot binary: %w", err)
		}
	}
	configFile, err := a.serviceConfigFile()
	if err != nil {
		return nil, err
	}
	m, err := a.newManager(p, binary, configFile, cmd.ErrOrStderr())
	if err != nil {
		return nil, err
	}
	if sum.installed, err = m.Install(cmd.Context()); err != nil {
		return nil, err
	}
	return m, nil
}

// installSummary is install's closing account of where everything is.
type installSummary struct {
	p          *paths.Paths
	cfg        *config.Config
	configFile string
	// configFlag means install ran with --config, which a re-run must
	// repeat or the service goes back to the default config.
	configFlag      bool
	serviceSocket   string
	configWritten   bool
	briefingWritten bool
	installed       *service.Installed
}

func (s *installSummary) write(out *errWriter, goos string) {
	out.printf("slop-bot is installed. Where things are:\n")
	cfgNote := written(s.configWritten, "a commented default", "yours, kept")
	var briefing []string
	switch {
	case s.cfg.BriefingPath == "":
		briefing = []string{"briefing", "(none)", "briefing_path is empty: agents get only slop-bot's own"}
	case s.cfg.BriefingPath == s.p.DefaultBriefing():
		briefing = []string{"briefing", s.cfg.BriefingPath, written(s.briefingWritten, "a stub to fill in", "yours, kept")}
	default:
		briefing = []string{"briefing", s.cfg.BriefingPath, "yours (briefing_path)"}
	}
	writeTable(out, [][]string{
		{"config", s.configFile, cfgNote},
		briefing,
		{"tickets db", s.p.TicketsDB()},
		{"state db", s.p.StateDB()},
		{"logs", service.LogsAt(goos, s.p.LogDir())},
		s.socketRow(),
		{"workspace_dir", s.cfg.WorkspaceDir, "worktrees; set in the config"},
		{"service", s.installed.UnitPath, "runs: " + strings.Join(append([]string{s.installed.Binary}, s.installed.Args...), " ")},
	})

	out.printf("\nXDG variables (from this shell; written into the service):\n")
	var xdg [][]string
	for _, d := range []paths.Dir{s.p.Config, s.p.Data, s.p.State, s.p.Cache, s.p.Runtime} {
		how := map[paths.Source]string{paths.FromEnv: "set", paths.FromDefault: "defaulted",
			paths.FromFallback: "fallback"}[d.Source]
		xdg = append(xdg, []string{d.Var, how, filepath.Dir(d.Path)})
	}
	writeTable(out, xdg)
	if goos == "linux" {
		out.printf("  (systemd gives the service its own XDG_RUNTIME_DIR, /run/user/<uid>)\n")
	}
	if line := lingerLine(s.installed.Linger); line != "" {
		out.printf("\n%s\n", line)
	}
	for _, warn := range s.installed.Warnings {
		out.printf("warning: %s\n", warn)
	}
	rerun := "slop-bot install"
	if s.configFlag {
		rerun = "slop-bot --config " + s.configFile + " install"
	}
	out.printf("\nIf these don't suit:\n")
	out.printf("  - to move a location, export its XDG_* variable in your shell profile and re-run `%s`:\n"+
		"    the service takes its paths from the shell that installs it", rerun)
	if goos == "linux" {
		out.printf(" (all but XDG_RUNTIME_DIR, which systemd sets)")
	}
	out.printf("\n  - edit the config (repos, harnesses, workspace_dir) and the briefing, then run `slop-bot doctor`;\n"+
		"    re-running `%s` keeps both and restarts the service\n"+
		"  - `slop-bot uninstall` removes the service; add --purge to delete slop-bot's data too\n", rerun)
	out.printf("\nCheck it came up: slop-bot health\n")
}

// writeTable prints indented, aligned rows with no trailing padding: rows
// may be shorter than others.
func writeTable(out *errWriter, rows [][]string) {
	var widths []int
	for _, r := range rows {
		for i, c := range r {
			if i >= len(widths) {
				widths = append(widths, 0)
			}
			widths[i] = max(widths[i], len(c))
		}
	}
	for _, r := range rows {
		var b strings.Builder
		for i, c := range r {
			if i < len(r)-1 {
				fmt.Fprintf(&b, "%-*s  ", widths[i], c)
			} else {
				b.WriteString(c)
			}
		}
		out.printf("  %s\n", b.String())
	}
}

func (s *installSummary) socketRow() []string {
	if s.serviceSocket == "" || s.serviceSocket == s.p.Socket() {
		return []string{"socket", s.p.Socket()}
	}
	return []string{"socket", s.serviceSocket, "the service's; this shell resolves " + s.p.Socket()}
}

func written(did bool, what, kept string) string {
	if did {
		return "written: " + what
	}
	return kept
}

func (a *app) uninstallCmd() *cobra.Command {
	var purge, yes bool
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Stop and remove the service; --purge also deletes slop-bot's data",
		Long: "Stop and remove the service. Your config, briefing, tickets and state stay " +
			"unless --purge, which lists what it will delete and asks first. workspace_dir " +
			"is never deleted: it may hold work in progress.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return a.uninstall(cmd, purge, yes) },
	}
	cmd.Flags().BoolVar(&purge, "purge", false, "also delete slop-bot's config, briefing, tickets, state and cache")
	cmd.Flags().BoolVar(&yes, "yes", false, "with --purge, delete without asking")
	return cmd
}

func (a *app) uninstall(cmd *cobra.Command, purge, yes bool) error {
	out := &errWriter{w: cmd.OutOrStdout()}
	p, err := a.paths()
	if err != nil {
		return err
	}
	m, err := a.newManager(p, "", "", cmd.ErrOrStderr())
	if err != nil {
		return err
	}
	// Everything purge needs is settled, and confirmed, before the
	// service goes: the unit is the record of where its data is.
	var targets []string
	if purge {
		if targets, err = a.purgeTargets(out, m, p); err != nil {
			return err
		}
		if _, err := m.ServiceEnv(); errors.Is(err, service.ErrNotInstalled) && a.daemonAnswers(cmd.Context(), p.Socket()) {
			// No service to stop, so the daemon was started by hand: say so
			// before asking, rather than after.
			return fmt.Errorf("a slop-bot daemon is still running at %s; stop it, then purge", p.Socket())
		}
		if len(targets) > 0 && !yes {
			out.printf("type 'purge' to delete them: ")
			if !confirmed(cmd.Context(), a.deps.Stdin, "purge") {
				return errNotConfirmed
			}
		}
	}
	switch err := m.Uninstall(cmd.Context()); {
	case errors.Is(err, service.ErrNotInstalled):
		out.printf("the slop-bot service isn't installed (no %s)\n", m.UnitPath())
	case err != nil:
		return err
	default:
		out.printf("stopped and removed the slop-bot service: %s\n", m.UnitPath())
	}
	if !purge {
		out.printf("kept slop-bot's files (config, tickets, state); `slop-bot uninstall --purge` deletes them\n")
		return out.err
	}
	// The service's daemon is stopped now. One that still answers was
	// started by hand, and would go on writing into what's deleted.
	if len(targets) > 0 && a.daemonAnswers(cmd.Context(), p.Socket()) {
		return fmt.Errorf("a slop-bot daemon started by hand is still running at %s, so nothing was deleted; "+
			"stop it, then run `slop-bot uninstall --purge` again", p.Socket())
	}
	for _, t := range targets {
		if err := os.RemoveAll(t); err != nil {
			return fmt.Errorf("purging %s: %w", t, err)
		}
	}
	if len(targets) > 0 {
		out.printf("deleted %d directories\n", len(targets))
	}
	return out.err
}

// errNotConfirmed is a purge the user didn't confirm.
var errNotConfirmed = errors.New("not confirmed; nothing was deleted and the service is still installed")

// purgeTargets are slop-bot's own directories, each <XDG base>/slop-bot,
// that exist: nothing else under a base is touched. It refuses when the
// directories might not be the right ones, or would take workspace_dir
// (with work in progress) with them.
func (a *app) purgeTargets(out *errWriter, m service.Manager, p *paths.Paths) ([]string, error) {
	// This shell's directories may not be the service's: purge from the
	// shell that installed it, or the real data stays and another
	// instance's goes.
	if svc, err := service.ServicePaths(m); err == nil {
		if mm := service.Mismatches(p, svc); len(mm) > 0 {
			return nil, fmt.Errorf("this shell's %s is %s but the installed service's is %s; purge from the shell "+
				"that installed it", mm[0].What, mm[0].CLI, mm[0].Service)
		}
	}
	c, err := a.loadConfig(p)
	if err != nil {
		return nil, fmt.Errorf("can't tell where workspace_dir is, so nothing is purged: %w", err)
	}
	var targets []string
	for _, d := range []paths.Dir{p.Config, p.Data, p.State, p.Cache} {
		if _, err := os.Stat(d.Path); err != nil {
			continue
		}
		if within(c.WorkspaceDir, d.Path) {
			return nil, fmt.Errorf("workspace_dir %s is inside %s, which --purge would delete with any work "+
				"in progress; move workspace_dir out first, or delete by hand", c.WorkspaceDir, d.Path)
		}
		targets = append(targets, d.Path)
	}
	if len(targets) == 0 {
		out.printf("nothing to purge\n")
		return nil, nil
	}
	out.printf("--purge deletes:\n")
	for _, t := range targets {
		out.printf("  %s\n", t)
	}
	out.printf("and leaves workspace_dir alone (%s): it may hold work in progress\n", c.WorkspaceDir)
	return targets, nil
}

// daemonAnswers reports whether a daemon answers at socket.
func (a *app) daemonAnswers(ctx context.Context, socket string) bool {
	if a.deps.daemonAnswers != nil {
		return a.deps.daemonAnswers(ctx, socket)
	}
	conn, err := control.Dialer(socket)(ctx, "", "")
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// within reports whether path is dir or under it, after symlinks, so a
// link can't hide a workspace inside a purged directory.
func within(path, dir string) bool {
	path, dir = realPath(path), realPath(dir)
	rel, err := filepath.Rel(dir, path)
	return err == nil && (rel == "." || filepath.IsLocal(rel))
}

// realPath resolves symlinks in the longest prefix of path that exists.
func realPath(path string) string {
	rest := ""
	for p := filepath.Clean(path); ; p = filepath.Dir(p) {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(r, rest)
		}
		if parent := filepath.Dir(p); parent == p {
			return path
		}
		rest = filepath.Join(filepath.Base(p), rest)
	}
}

// confirmed reads the answer to a prompt. Ctrl-C cancels ctx, and is a no:
// main's signal handling consumes the interrupt, so a read that ignored ctx
// would wait on for Enter, then go ahead.
func confirmed(ctx context.Context, in io.Reader, word string) bool {
	if in == nil {
		return false
	}
	answer := make(chan string, 1)
	go func() {
		// Left blocked on a cancelled prompt; the process is about to exit.
		line, _ := bufio.NewReader(in).ReadString('\n')
		answer <- line
	}()
	select {
	case <-ctx.Done():
		return false
	case line := <-answer:
		return ctx.Err() == nil && strings.TrimSpace(line) == word
	}
}
