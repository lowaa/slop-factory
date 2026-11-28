package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/lowaa/slop-factory/slop-bot/internal/paths"
	"github.com/lowaa/slop-factory/slop-bot/internal/service"
)

func (a *app) serviceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "service",
		Short: "Install and control the slop-bot daemon as a per-user service",
		Long: "Install and control the daemon as a systemd user unit (Linux) or a " +
			"launchd agent (macOS). The unit runs `slop-bot daemon` with the paths " +
			"and PATH resolved from this shell.",
	}
	cmd.AddCommand(a.serviceInstallCmd(), a.serviceUninstallCmd(), a.serviceStatusCmd(),
		a.serviceActionCmd("start", "Start the installed service", service.Manager.Start),
		a.serviceActionCmd("stop", "Stop the service; it starts again at the next boot or login",
			service.Manager.Stop),
	)
	return cmd
}

// serviceManager builds the Manager. binary matters only to install; the
// other commands pass "".
func (a *app) serviceManager(cmd *cobra.Command, binary string) (service.Manager, *paths.Paths, error) {
	p, err := a.paths()
	if err != nil {
		return nil, nil, err
	}
	configFile, err := a.serviceConfigFile()
	if err != nil {
		return nil, nil, err
	}
	path, _ := a.deps.Env.Lookup("PATH")
	m, err := service.New(&service.Options{
		Paths: p, Home: a.deps.Env.Home, UID: a.deps.Env.UID, GOOS: a.deps.Env.GOOS,
		Binary: binary, PATH: path, ConfigFile: configFile,
		Runner: a.deps.ServiceRunner,
		Logger: slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), &slog.HandlerOptions{Level: slog.LevelWarn})),
	})
	return m, p, err
}

// serviceConfigFile is --config made absolute, so the service runs with
// the config the user named rather than silently with the default.
func (a *app) serviceConfigFile() (string, error) {
	if a.configPath == "" {
		return "", nil
	}
	abs, err := filepath.Abs(a.configPath)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(abs); err != nil {
		return "", fmt.Errorf("--config: %w", err)
	}
	return abs, nil
}

func (a *app) serviceInstallCmd() *cobra.Command {
	var binary string
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Write the service unit, then enable and (re)start it",
		Long: "Write the systemd unit or launchd plist, then enable and (re)start it. " +
			"Re-run it after changing XDG_* variables or PATH, so the service picks them up.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if binary == "" {
				var err error
				if binary, err = a.deps.FindBinary(); err != nil {
					return fmt.Errorf("finding the slop-bot binary: %w", err)
				}
			}
			m, p, err := a.serviceManager(cmd, binary)
			if err != nil {
				return err
			}
			res, err := m.Install(cmd.Context())
			if err != nil {
				return err
			}
			return printInstalled(cmd.OutOrStdout(), p, res)
		},
	}
	cmd.Flags().StringVar(&binary, "binary", "",
		"absolute path of the slop-bot binary the service runs (default: this one)")
	return cmd
}

func printInstalled(out io.Writer, p *paths.Paths, res *service.Installed) error {
	w := &errWriter{w: out}
	w.printf("installed the slop-bot service: %s\n", res.UnitPath)
	w.printf("it runs: %s %s\n", res.Binary, strings.Join(res.Args, " "))
	w.printf("environment written into it:\n")
	// Not aligned: PATH is long enough to push every other origin off the
	// screen.
	for _, e := range res.Env {
		w.printf("  %s=%s  (%s)\n", e.Name, e.Value, envOrigin(p, e.Name))
	}
	switch res.Linger {
	case service.LingerOff:
		w.printf("linger: off. systemd stops slop-bot when your last session ends; " +
			"to keep it running with nobody logged in (a headless box), run: loginctl enable-linger\n")
	case service.LingerOn:
		w.printf("linger: on. slop-bot keeps running with nobody logged in\n")
	case service.LingerUnknown:
		w.printf("linger: unknown (loginctl couldn't say). On a headless box, check `loginctl show-user $USER -p Linger`\n")
	}
	for _, warn := range res.Warnings {
		w.printf("warning: %s\n", warn)
	}
	// The service manager reports the process started, not that the daemon
	// came up: a bad config or a held lock only shows after.
	w.printf("check it came up: slop-bot health (or slop-bot service status)\n")
	return w.err
}

// envOrigin says where a written variable's value came from.
func envOrigin(p *paths.Paths, name string) string {
	if name == "PATH" {
		return "your PATH now"
	}
	for _, d := range []paths.Dir{p.Config, p.Data, p.State, p.Cache, p.Runtime} {
		if d.Var == name {
			return origin(d)
		}
	}
	return ""
}

func (a *app) serviceUninstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Stop and disable the service, and remove its unit",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			m, _, err := a.serviceManager(cmd, "")
			if err != nil {
				return err
			}
			err = m.Uninstall(cmd.Context())
			if errors.Is(err, service.ErrNotInstalled) {
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "the slop-bot service isn't installed (no %s)\n", m.UnitPath())
				return err
			}
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "uninstalled the slop-bot service: removed %s\n", m.UnitPath())
			return err
		},
	}
}

// serviceActionCmd is start or stop: act takes the method expression.
func (a *app) serviceActionCmd(use, short string, act func(service.Manager, context.Context) error) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			m, _, err := a.serviceManager(cmd, "")
			if err != nil {
				return err
			}
			err = act(m, cmd.Context())
			if errors.Is(err, service.ErrNotInstalled) {
				return fmt.Errorf("%w; run `slop-bot service install` first", err)
			}
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "slop-bot service: %s done\n", use)
			return err
		},
	}
}

func (a *app) serviceStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show whether the service is installed, enabled and running",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			m, _, err := a.serviceManager(cmd, "")
			if err != nil {
				return err
			}
			st, err := m.Status(cmd.Context())
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			rows := [][2]string{
				{"unit", st.UnitPath},
				{"installed", yesNo(st.Installed)},
				{"enabled", yesNo(st.Enabled)},
				{"running", fmt.Sprintf("%s (%s)", yesNo(st.Running), st.State)},
			}
			if st.Linger != service.LingerNotApplicable {
				rows = append(rows, [2]string{"linger", string(st.Linger)})
			}
			for _, r := range rows {
				if _, err := fmt.Fprintf(w, "%s\t%s\n", r[0], r[1]); err != nil {
					return err
				}
			}
			return w.Flush()
		},
	}
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// errWriter keeps the first write error, so a run of prints checks once.
type errWriter struct {
	w   io.Writer
	err error
}

func (e *errWriter) Write(b []byte) (int, error) {
	if e.err != nil {
		return 0, e.err
	}
	n, err := e.w.Write(b)
	e.err = err
	return n, err
}

func (e *errWriter) printf(format string, args ...any) { _, _ = fmt.Fprintf(e, format, args...) }
