package main

import (
	"github.com/lowaa/slop-factory/slop-haus/server/internal/apiserver"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/wakeup"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/worker"
)

// workerModule answers the supervisors' worker polls; the waiter holds
// polls open until shutdown.
type workerModule struct {
	Waiter  *wakeup.Waiter
	Handler *worker.Handler
}

func newWorkerModule(d *moduleDeps) (module, error) {
	c, s, ch := d.container, d.svcs, lookup[*codeHostModule](d.built)
	waiter := wakeup.New(wakeup.Deps{
		Tickets:    s.tickets,
		Messages:   lookup[*messagesModule](d.built).Service,
		Policy:     s.settings,
		LockStates: s.tickets,
		Polls:      wakeup.NewPostgresStore(c.database),
		Slots:      s.agents,
		Archive:    c.tenants,
	}, nil, c.logHandler)
	return &workerModule{Waiter: waiter,
		Handler: worker.NewHandler(worker.NewService(s.tickets, ch.Changes, lookup[*reviewReportModule](d.built).Service, c.logHandler), waiter, lookup[*supervisorModule](d.built).Service, lookup[*agentModule](d.built).Layouts, c.logHandler)}, nil
}

func (m *workerModule) Routes(r *apiserver.Deps) { r.WorkerHandler = m.Handler }

// Close lets the polls held open return.
func (m *workerModule) Close() { m.Waiter.Close() }
