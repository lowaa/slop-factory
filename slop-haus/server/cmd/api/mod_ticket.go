package main

import (
	"github.com/lowaa/slop-factory/slop-haus/server/internal/apiserver"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/attemptsignals"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/attemptsignals/usage"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/attemptverdict"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/change"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticketusage"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/variantcompare"
)

// ticketModule is the board's tickets: the handler (and the promotions it
// carries to the code host after their response), the live board stream and
// the attempts' costs.
type ticketModule struct {
	Handler      *ticket.Handler
	BoardStream  *ticket.BoardStream
	UsageHandler *ticketusage.Handler
	Verdicts     *attemptverdict.Handler
	Compare      *variantcompare.Handler
}

func newTicketModule(d *moduleDeps) (module, error) {
	c, s, ch := d.container, d.svcs, lookup[*codeHostModule](d.built)
	costs := ticketusage.NewPostgresStore(c.database)
	signals := attemptsignals.NewService(attemptsignals.NewStore(c.database), usage.NewStore(c.database),
		attemptsignals.NewHostSource(c.database, change.NewPostgresStore(c.database), s.hosts), c.logHandler, nil)
	verdicts := attemptverdict.NewStore(c.database)
	compare := variantcompare.NewService(variantcompare.NewPostgresSource(c.database), attemptsignals.NewStore(c.database), costs, verdicts)
	return &ticketModule{
		Handler: ticket.NewHandler(s.tickets, c.logHandler,
			ticket.WithAttemptsAdd(c.cfg.AttemptsAddEnabled), ticket.WithReplays(c.cfg.ReplaysEnabled),
			ticket.WithAttemptCosts(costs), ticket.WithAttemptSignals(signals), ticket.WithAttemptVerdicts(verdicts), ticket.WithAttemptDiscussions(ticket.NewPostgresAttemptDiscussions(c.database)),
			ticket.WithAttemptChangeCloser(ch.Changes), ticket.WithAttemptPromotion(c.cfg.AttemptsPromoteEnabled, ch.Changes)),
		// Its lifetime is the access token's: see BoardStreamOptions.
		BoardStream: ticket.NewBoardStream(c.ticketStore,
			&ticket.BoardStreamOptions{MaxLifetime: c.cfg.AccessTokenTTL, Costs: costs}, c.logHandler),
		UsageHandler: ticketusage.NewHandler(costs, c.logHandler),
		Verdicts:     attemptverdict.NewHandler(attemptverdict.NewService(verdicts, c.events), c.logHandler),
		Compare:      variantcompare.NewHandler(compare, c.logHandler),
	}, nil
}

func (m *ticketModule) Routes(r *apiserver.Deps) {
	r.TicketHandler = m.Handler
	r.BoardStream = m.BoardStream
	r.TicketUsageHandler = m.UsageHandler
	r.AttemptVerdictHandler = m.Verdicts
	r.VariantCompareHandler = m.Compare
}

// Close is empty: the handler's promotions are waited for after the servers
// stop and the database is still open (see serve), not when the waits drain.
func (*ticketModule) Close() {}
