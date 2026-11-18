package main

import (
	usagelib "github.com/lowaa/slop-factory/go-llm-usage"
	"github.com/lowaa/slop-factory/go-llm-usage/pricing"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/apiserver"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/config"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/llmusage"
)

// llmUsageModule is the usage hook's service, its llm_* counters served with
// the rest of the metrics.
type llmUsageModule struct {
	Service *llmusage.Service
	Handler *llmusage.Handler
}

func newLLMUsageModule(d *moduleDeps) (module, error) {
	prices := pricing.Default()
	collector, err := usagelib.NewCollector(&usagelib.CollectorOptions{Models: prices})
	if err != nil {
		return nil, err
	}
	if err := d.limits.telemetry.Register(collector); err != nil {
		return nil, err
	}
	svc, err := llmusage.NewService(&llmusage.Options{
		Store:     llmusage.NewPostgresStore(d.database, prices),
		Collector: collector,
		Pricer:    prices,
		Limit:     d.limits.limit(config.LLMUsageLimit()),
	}, d.logHandler)
	if err != nil {
		return nil, err
	}
	return &llmUsageModule{Service: svc, Handler: llmusage.NewHandler(svc, d.logHandler)}, nil
}

func (m *llmUsageModule) Routes(r *apiserver.Deps) { r.UsageHandler = m.Handler }

func (*llmUsageModule) Close() {}
