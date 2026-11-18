package main

import (
	"github.com/lowaa/slop-factory/slop-haus/server/internal/apiserver"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/conductor"
)

type conversationsModule struct {
	Service *conductor.Service
	Handler *conductor.Handler
}

func newConversationsModule(d *moduleDeps) (module, error) {
	c, s := d.container, d.svcs
	svc := conductor.NewService(&conductor.Options{
		Store:   conductor.NewPostgresStore(c.database),
		Scopes:  s.tickets,
		Layouts: lookup[*agentModule](d.built).Layouts,
		// Loaded again here, as the package handler does: the board is built after the modules.
		CurrentHostVersion: shippedPackageVersion(c.cfg.SupervisorPackageDir),
	}, c.logHandler)
	return &conversationsModule{Service: svc, Handler: conductor.NewHandler(svc, s.conductors,
		// Like the board's stream, its lifetime is the access token's.
		&conductor.StreamOptions{MaxLifetime: c.cfg.AccessTokenTTL}, c.logHandler)}, nil
}

func (m *conversationsModule) Routes(r *apiserver.Deps) { r.ConductorHandler = m.Handler }

// Close ends the conductors' polls and streams, which shutdown would
// otherwise wait on.
func (m *conversationsModule) Close() { m.Service.Close() }
