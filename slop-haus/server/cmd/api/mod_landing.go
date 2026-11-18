package main

import (
	"github.com/lowaa/slop-factory/slop-haus/server/internal/apiserver"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/change"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/landing"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/mcpserver"
)

// landingModule is the merge queue's board side: the status handler and what
// ticket_finish enqueues through. The runner that drains the queue is started
// later (see startLanding), once the board exists.
type landingModule struct {
	Handler *landing.Handler
	// Finisher is nil with the MERGE_QUEUE_ENABLED kill switch off, which
	// leaves every tenant on the worker flow.
	Finisher *landing.Finisher
	// mode is nil with the kill switch off, which leaves every tenant its
	// lock tools.
	mode landing.Mode
}

func newLandingModule(d *moduleDeps) (module, error) {
	c, s, ch := d.container, d.svcs, lookup[*codeHostModule](d.built)
	cfg := c.cfg
	m := &landingModule{
		Handler: landing.NewHandler(landing.NewStatus(landing.NewPostgresQueue(c.database), s.settings, ch.Credentials, cfg.MergeQueueEnabled), c.logHandler),
	}
	if !cfg.MergeQueueEnabled {
		return m, nil
	}
	mode := landing.NewSettingsMode(s.settings, cfg.MergeQueueEnabled)
	f := landing.NewFinisher(s.tickets,
		landing.NewPostgresHost(change.NewPostgresStore(c.database), s.hosts), mode)
	f.SetGuard(landing.NewEnqueueGuard(landing.NewStore(c.database), s.tickets, c.events))
	m.Finisher, m.mode = f, mode
	return m, nil
}

func (m *landingModule) Routes(r *apiserver.Deps) { r.LandingHandler = m.Handler }

func (*landingModule) Close() {}

// Lander is what the MCP server finishes tickets through; nil, not a nil
// *Finisher in the interface, when the kill switch is off.
func (m *landingModule) Lander() mcpserver.Lander {
	if m.Finisher == nil {
		return nil
	}
	return m.Finisher
}

// QueueMode is what the MCP server reads to hide the lock tools; nil when the
// kill switch is off.
func (m *landingModule) QueueMode() mcpserver.QueueMode {
	if m.mode == nil {
		return nil
	}
	return m.mode
}
