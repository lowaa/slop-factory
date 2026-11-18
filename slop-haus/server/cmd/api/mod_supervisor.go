package main

import (
	"log/slog"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/apiserver"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/supervisorpkg"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/supervisorwatch"
)

// supervisorModule is the supervisors' heartbeats and the package they are
// shipped. It is first in moduleBuilders: provisioning and the worker polls
// report to its service.
type supervisorModule struct {
	Service        *supervisorwatch.Service
	Handler        *supervisorwatch.Handler
	PackageHandler *supervisorpkg.Handler
}

func newSupervisorModule(d *moduleDeps) (module, error) {
	c, s := d.container, d.svcs
	svc := supervisorwatch.NewService(supervisorwatch.NewPostgresStore(c.database), s.settings, s.tickets)
	logger := slog.New(c.logHandler)
	return &supervisorModule{
		Service: svc,
		Handler: supervisorwatch.NewHandler(svc, c.logHandler),
		PackageHandler: supervisorpkg.NewHandler(loadSupervisorPackage(c.cfg.SupervisorPackageDir, logger), agent.BaseURLFrom,
			tenantNameOf(c.users), logger),
	}, nil
}

func (m *supervisorModule) Routes(r *apiserver.Deps) {
	r.SupervisorHandler = m.Handler
	r.SupervisorPackageHandler = m.PackageHandler
}

func (*supervisorModule) Close() {}
