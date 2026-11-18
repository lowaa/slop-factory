package main

import (
	"github.com/lowaa/slop-factory/slop-haus/server/internal/apiserver"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/config"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/usagemetrics"
)

// usageModule is the counters for the board UI's usage events, served with
// the rest of the metrics. The api's own operation and tool counters are
// built with the board (board.go).
type usageModule struct {
	Handler *usagemetrics.UIHandler
}

func newUsageModule(d *moduleDeps) (module, error) {
	ui := usagemetrics.NewUI()
	if err := d.limits.telemetry.Register(ui.Collectors()...); err != nil {
		return nil, err
	}
	return &usageModule{Handler: usagemetrics.NewUIHandler(ui, d.limits.limit(config.UIUsageLimit()))}, nil
}

func (m *usageModule) Routes(r *apiserver.Deps) { r.UIUsageHandler = m.Handler }

func (*usageModule) Close() {}
