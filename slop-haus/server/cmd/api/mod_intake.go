package main

import (
	"github.com/lowaa/slop-factory/slop-haus/server/internal/apiserver"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/intake"
)

// intakeModule is the intake service, with the door shut unless
// INTAKE_LISTEN_ADDR is configured.
type intakeModule struct {
	Service *intake.Service
	Handler *intake.Handler
}

func newIntakeModule(d *moduleDeps) (module, error) {
	inCfg := &intake.Config{Archive: d.tenants, Owners: d.users}
	if in := d.cfg.Intake; in != nil {
		inCfg = &intake.Config{
			Enabled: true, PublicBaseURL: in.PublicBaseURL, Archive: d.tenants, Owners: d.users,
			Limiter: intake.NewRateLimiter(in.RatePerMinute, in.Burst),
		}
		if in.Key != "" {
			secrets, err := intake.NewSecrets([]byte(in.Key))
			if err != nil {
				return nil, err
			}
			inCfg.Secrets = secrets
		}
	}
	svc := intake.NewService(intake.NewPostgresStore(d.database), d.svcs.tickets, inCfg, d.events, d.logHandler)
	return &intakeModule{Service: svc, Handler: intake.NewHandler(svc, d.logHandler)}, nil
}

func (m *intakeModule) Routes(r *apiserver.Deps) { r.IntakeHandler = m.Handler }

func (*intakeModule) Close() {}
