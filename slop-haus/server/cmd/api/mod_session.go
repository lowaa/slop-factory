package main

import (
	"github.com/lowaa/slop-factory/slop-haus/server/internal/apiserver"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/hookserver"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/session"
)

// sessionModule is the agents' sessions and the hook endpoints that report
// them. The hooks read the layouts, so it follows agentModule.
type sessionModule struct {
	Service *session.Service
	Hooks   *hookserver.Handler
}

func newSessionModule(d *moduleDeps) (module, error) {
	c, s := d.container, d.svcs
	svc := session.NewService(session.NewPostgresStore(c.database), c.logHandler)
	return &sessionModule{Service: svc, Hooks: hookserver.NewHandler(hookserver.Deps{
		Tickets:  s.tickets,
		Messages: lookup[*messagesModule](d.built).Service,
		Sessions: svc,
		Layouts:  lookup[*agentModule](d.built).Layouts,
		Archive:  c.tenants,
	}, c.logHandler)}, nil
}

func (m *sessionModule) Routes(r *apiserver.Deps) { r.HookHandler = m.Hooks }

func (*sessionModule) Close() {}
