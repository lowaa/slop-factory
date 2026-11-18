package main

import (
	"github.com/lowaa/slop-factory/slop-haus/server/internal/apiserver"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/pendingaction"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward/agreement"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward/discussionanswer"
)

// stewardModule is the steward's board side: the proposals, digest and
// discussion-answer handlers. The services behind them stay in newServices:
// the agent, change and MCP wiring are built from the pending and steward
// services before any module, and they close a cycle through lateDeps.
type stewardModule struct {
	Proposals         *pendingaction.Handler
	Digest            *steward.DigestHandler
	DiscussionAnswers *discussionanswer.Handler
}

func newStewardModule(d *moduleDeps) (module, error) {
	s, users, logHandler := d.svcs, d.users, d.logHandler
	return &stewardModule{
		Proposals: pendingaction.NewHandler(s.pending, logHandler).WithStewardControls(&stewardControlsAdapter{controls: s.stewardControls}).
			WithDraftApplier(&draftApplier{assist: s.stewardAssist, roles: users}),
		Digest: steward.NewDigestHandler(steward.NewDigester(s.stewardStore, steward.NewTenantSettings(s.settings), agreement.ForDigest, nil).
			WithDiscussions(agreement.NewDigestSource(agreement.NewScorer(s.stewardStore, d.database))), logHandler),
		DiscussionAnswers: discussionanswer.NewHandler(s.discussionAnswers, users, logHandler).WithApplying(s.discussionApply),
	}, nil
}

func (m *stewardModule) Routes(r *apiserver.Deps) {
	r.ProposalHandler = m.Proposals
	r.StewardDigestHandler = m.Digest
	r.DiscussionAnswerHandler = m.DiscussionAnswers
}

func (*stewardModule) Close() {}
