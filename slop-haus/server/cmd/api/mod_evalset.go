package main

import (
	"github.com/lowaa/slop-factory/slop-haus/server/internal/apiserver"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/evalset"
)

type evalSetModule struct {
	Handler *evalset.Handler
}

func newEvalSetModule(d *moduleDeps) (module, error) {
	c := d.container
	svc := evalset.NewService(evalset.NewPostgresStore(c.database), d.svcs.tickets, c.cfg.AttemptsAddEnabled && c.cfg.ReplaysEnabled, c.logHandler)
	return &evalSetModule{Handler: evalset.NewHandler(svc, c.logHandler)}, nil
}

func (m *evalSetModule) Routes(r *apiserver.Deps) { r.EvalSetHandler = m.Handler }

func (*evalSetModule) Close() {}
