package main

import (
	"github.com/lowaa/slop-factory/slop-haus/server/internal/apiserver"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/change"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/githubapp"
)

// built on them. The change flow's closing rule is bound to the ticket
// service here, as the service exists only now.
type codeHostModule struct {
	Credentials *githubapp.Service
	Handler     *githubapp.Handler
	Changes     *change.Service
}

func newCodeHostModule(d *moduleDeps) (module, error) {
	c, s := d.container, d.svcs
	ws := lookup[*workspaceModule](d.built)
	creds, client, err := githubAppCredentials(c, ws.Service)
	if err != nil {
		return nil, err
	}
	changes, err := changeService(c, s, client, creds, d.hostChanges)
	if err != nil {
		return nil, err
	}
	// The closing rule runs after every transition that moves a ticket or one
	// of its attempts.
	c.settled.Bind(changes.TicketsSettled)
	return &codeHostModule{
		Credentials: creds, Handler: githubapp.NewHandler(creds, c.logHandler), Changes: changes,
	}, nil
}

func (m *codeHostModule) Routes(r *apiserver.Deps) {
	r.GitHubAppHandler = m.Handler
}

func (*codeHostModule) Close() {}
