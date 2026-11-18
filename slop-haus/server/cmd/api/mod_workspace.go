package main

import (
	"github.com/lowaa/slop-factory/slop-haus/server/internal/apiserver"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/manifest"
)

type workspaceModule struct {
	Service *manifest.Service
	Handler *manifest.Handler
}

func newWorkspaceModule(d *moduleDeps) (module, error) {
	c, s := d.container, d.svcs
	svc := manifest.NewService(c.manifestStore, c.users, s.agents, lookup[*agentModule](d.built).Layouts, c.logHandler,
		manifest.WithHosts(s.hosts), manifest.WithAudit(c.events), manifest.WithSync(c.ticketStore, s.tickets))
	return &workspaceModule{Service: svc, Handler: manifest.NewHandler(svc, c.logHandler)}, nil
}

func (m *workspaceModule) Routes(r *apiserver.Deps) { r.ManifestHandler = m.Handler }

func (*workspaceModule) Close() {}
