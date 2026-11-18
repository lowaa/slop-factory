package main

import (
	"github.com/lowaa/slop-factory/slop-haus/server/internal/apiserver"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/reports"
)

type reportsModule struct {
	Handler *reports.Handler
}

func newReportsModule(d *moduleDeps) (module, error) {
	c := d.container
	svc := reports.NewService(reports.NewPostgresStore(c.database, 0), nil)
	return &reportsModule{Handler: reports.NewHandler(svc, c.logHandler)}, nil
}

func (m *reportsModule) Routes(r *apiserver.Deps) { r.ReportsHandler = m.Handler }

func (*reportsModule) Close() {}
