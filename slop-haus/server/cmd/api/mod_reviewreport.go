package main

import (
	"github.com/lowaa/slop-factory/slop-haus/server/internal/apiserver"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/config"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/reviewreport"
)

// reviewReportModule is the report server slop-review posts its runs to.
type reviewReportModule struct {
	Service *reviewreport.Service
	Handler *reviewreport.Handler
}

func newReviewReportModule(d *moduleDeps) (module, error) {
	c := d.container
	svc, err := reviewreport.NewService(&reviewreport.Options{
		Store:    reviewreport.NewPostgresStore(c.database),
		Settings: d.svcs.settings,
		Limit:    c.limits.limit(config.ReviewReportLimit()),
		BoardURL: c.cfg.PublicBaseURL,
	}, c.logHandler)
	if err != nil {
		return nil, err
	}
	return &reviewReportModule{Service: svc, Handler: reviewreport.NewHandler(svc, c.logHandler)}, nil
}

func (m *reviewReportModule) Routes(r *apiserver.Deps) { r.ReviewReportHandler = m.Handler }

func (*reviewReportModule) Close() {}
