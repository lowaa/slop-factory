package ticket_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
)

// Only a worker polls for work, so only a worker's asker is worth holding a
// ticket for. A console acts when its human types and a conductor is never
// sent to claim, so a hold for them would idle the human's answer for the
// whole window.
func TestAnswer_FirstClaimIsOwedOnlyToAnEnabledWorker(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		// become turns the asker into this kind of agent before the human
		// answers; kind is immutable, so a conductor is another agent
		// recorded as the asker.
		become func(t *testing.T, f *fixture, ticketID int64, asker uuid.UUID)
		held   bool
	}{
		"worker": {held: true},
		"console": {become: func(t *testing.T, f *fixture, ticketID int64, _ uuid.UUID) {
			console, err := f.agents.CreateKind(f.ctx, "console-kind", agent.KindConsole)
			require.NoError(t, err)
			f.run(t, `UPDATE attempts SET resume_agent_id = $2 WHERE ticket_id = $1 AND number = 1`, ticketID, console.ID)
		}},
		"conductor": {become: func(t *testing.T, f *fixture, ticketID int64, _ uuid.UUID) {
			conductor := uuid.New()
			_, err := f.database.Global().Exec(context.Background(),
				`INSERT INTO global_tenant_memberships (id, tenant, user_id, role) VALUES ($1, $2, $3, 'member')`,
				uuid.New(), f.tenant, f.human)
			require.NoError(t, err)
			f.run(t, `INSERT INTO agents (id, name, kind, owner_user_id) VALUES ($1, 'conductor', 'conductor', $2)`,
				conductor, f.human)
			f.run(t, `UPDATE attempts SET resume_agent_id = $2 WHERE ticket_id = $1 AND number = 1`, ticketID, conductor)
		}},
		"disabled worker": {become: func(t *testing.T, f *fixture, _ int64, asker uuid.UUID) {
			f.run(t, `UPDATE agents SET disabled_at = now() WHERE id = $1`, asker)
		}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			asker, other := f.agent(t, "asker"), f.agent(t, "other")
			created := f.ticket(t, "Loose ticket", "server/")
			f.claim(t, asker)
			f.discuss(t, asker)
			if tc.become != nil {
				tc.become(t, f, created.ID, asker)
			}

			f.humanReply(t, created.ID)

			stored, err := f.svc.Get(f.ctx, created.ID)
			require.NoError(t, err)
			require.NotNil(t, stored.ResumeAgentID, "who asked stays on record")
			assert.Equal(t, tc.held, stored.AffinityUntil != nil, "stored window")

			rec := send(t, f.boardMux(), http.MethodGet, fmt.Sprintf("/api/tickets/%d", created.ID), "")
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Equal(t, tc.held, decodeTicket(t, rec.Body.Bytes()).AffinityUntil != nil, "as the API returns it")

			// Peek and Claim must agree.
			peeked, err := f.svc.Peek(f.ctx, other, nil)
			require.NoError(t, err)
			assert.Equal(t, !tc.held, peeked.Ticket != nil, "peek by the other worker")
			claimed := f.claim(t, other)
			assert.Equal(t, !tc.held, claimed != nil, "claim by the other worker")
			if tc.held {
				peeked, err := f.svc.Peek(f.ctx, asker, nil)
				require.NoError(t, err)
				require.NotNil(t, peeked.Ticket, "peek by the worker asker")
				regained := f.claim(t, asker)
				require.NotNil(t, regained, "the worker asker claims its own ticket")
				assert.Equal(t, created.ID, regained.ID)
			}
		})
	}
}
