package main

import (
	"github.com/lowaa/slop-factory/slop-haus/server/internal/apiserver"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/fleetinstructions"
)

type fleetModule struct {
	Service *fleetinstructions.Service
	Handler *fleetinstructions.Handler
}

func newFleetModule(d *moduleDeps) (module, error) {
	c, ch := d.container, lookup[*codeHostModule](d.built)
	svc := fleetinstructions.NewService(fleetinstructions.NewPostgresStore(c.database), c.users, c.events, c.logHandler)
	return &fleetModule{Service: svc, Handler: fleetinstructions.NewHandler(svc, ch.Credentials, c.logHandler)}, nil
}

func (m *fleetModule) Routes(r *apiserver.Deps) { r.FleetHandler = m.Handler }

func (*fleetModule) Close() {}
