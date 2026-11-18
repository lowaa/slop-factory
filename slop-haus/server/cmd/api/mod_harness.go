package main

import (
	"github.com/lowaa/slop-factory/slop-haus/server/internal/apiserver"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/harnessconfig"
)

// harnessModule is the harness config endpoints. The service stays in
// newServices: the ticket service resolves lease config with it.
type harnessModule struct {
	Handler *harnessconfig.Handler
}

func newHarnessModule(d *moduleDeps) (module, error) {
	return &harnessModule{Handler: harnessconfig.NewHandler(d.svcs.harness, d.logHandler)}, nil
}

func (m *harnessModule) Routes(r *apiserver.Deps) { r.HarnessHandler = m.Handler }

func (*harnessModule) Close() {}
