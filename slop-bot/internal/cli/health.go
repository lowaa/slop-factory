package cli

import (
	"fmt"
	"net/http"

	"github.com/spf13/cobra"

	"github.com/lowaa/slop-factory/slop-bot/internal/api"
)

func (a *app) healthCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "health",
		Short: "Check that the daemon is running and answering",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			var h api.Health
			if err := c.Do(cmd.Context(), http.MethodGet, api.PathHealth, nil, &h); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "daemon %s running (pid %d) since %s on %s\n",
				h.Version, h.PID, h.StartedAt.Format("2006-01-02 15:04:05"), h.Socket)
			return err
		},
	}
}
