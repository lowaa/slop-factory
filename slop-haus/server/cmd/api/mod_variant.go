package main

import (
	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/apiserver"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/variant"
)

type variantModule struct {
	Handler *variant.Handler
}

func newVariantModule(d *moduleDeps) (module, error) {
	c := d.container
	svc := variant.NewService(variant.NewPostgresStore(c.database, agent.VariantWriter{}), c.users, c.events, c.logHandler)
	return &variantModule{Handler: variant.NewHandler(svc, c.logHandler)}, nil
}

func (m *variantModule) Routes(r *apiserver.Deps) { r.VariantHandler = m.Handler }

func (*variantModule) Close() {}
