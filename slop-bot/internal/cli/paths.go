package cli

import (
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/lowaa/slop-factory/slop-bot/internal/doctor"
	"github.com/lowaa/slop-factory/slop-bot/internal/paths"
)

func (a *app) pathsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "paths",
		Short: "Show where slop-bot keeps its files",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := a.paths()
			if err != nil {
				return err
			}
			cfgFile := p.ConfigFile()
			if a.configPath != "" {
				cfgFile = a.configPath
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			rows := [][3]string{
				{"config file", cfgFile, ""},
				{"config dir", p.Config.Path, origin(p.Config)},
				{"data dir", p.Data.Path, origin(p.Data)},
				{"local tickets db", p.TicketsDB(), ""},
				{"state dir", p.State.Path, origin(p.State)},
				{"state db", p.StateDB(), ""},
				{"logs", p.LogDir(), ""},
				{"cache dir", p.Cache.Path, origin(p.Cache)},
				{"runtime dir", p.Runtime.Path, origin(p.Runtime)},
				{"control socket", p.Socket(), ""},
			}
			if c, err := a.loadConfig(p); err == nil {
				rows = append(rows, [3]string{"workspace dir", c.WorkspaceDir, "config"})
				briefing := c.BriefingPath
				if briefing == "" {
					briefing = "(none)"
				}
				rows = append(rows, [3]string{"user briefing", briefing, "config"})
			}
			for _, r := range rows {
				if _, err := fmt.Fprintf(w, "%s\t%s\t%s\n", r[0], r[1], r[2]); err != nil {
					return err
				}
			}
			if err := w.Flush(); err != nil {
				return err
			}
			return a.printMismatches(cmd.OutOrStdout(), p)
		},
	}
}

// printMismatches warns when the installed service resolves a location
// differently from this shell: the 1.4 mismatch warning.
func (a *app) printMismatches(out io.Writer, p *paths.Paths) error {
	w := &errWriter{w: out}
	warns := a.mismatchWarnings(p)
	for _, warn := range warns {
		w.printf("warning: %s\n", warn)
	}
	if len(warns) > 0 {
		w.printf("fix: %s\n", doctor.MismatchFix)
	}
	return w.err
}

func origin(d paths.Dir) string {
	switch d.Source {
	case paths.FromEnv:
		return "$" + d.Var
	case paths.FromFallback:
		return "fallback ($" + d.Var + " unset)"
	default:
		return "default ($" + d.Var + " unset)"
	}
}
