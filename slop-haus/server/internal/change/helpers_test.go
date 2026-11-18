package change_test

import (
	"context"
	"log/slog"
	"strconv"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/codehosttest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/pendingaction"
)

func formatInt(n int64) string { return strconv.FormatInt(n, 10) }

func discard() slog.Handler { return slog.DiscardHandler }

func hostsOf(f *fixture) *codehost.Registry { return codehosttest.Registry(f.host) }

// hostsReadOnly is a registry whose host can be read but not acted on.
func hostsReadOnly(host *codehosttest.Fake) *codehost.Registry {
	reg := codehost.NewRegistry()
	reg.Register(host, nil)
	return reg
}

// proposalsFake answers the approvals the test gives it.
type proposalsFake struct{ approvers []uuid.UUID }

func (p proposalsFake) List(_ context.Context, _ *pendingaction.ListFilter) ([]pendingaction.Action, error) {
	var out []pendingaction.Action
	for i := range p.approvers {
		out = append(out, pendingaction.Action{Kind: pendingaction.KindReady, DecidedByUserID: &p.approvers[i]})
	}
	return out, nil
}
