package agent_test

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/agentmsg"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

type ownerCommentFixture struct {
	*ownerBoundFixture
	msgs *agentmsg.Service
}

func newOwnerCommentFixture(t *testing.T) *ownerCommentFixture {
	t.Helper()
	f := newOwnerBoundFixture(t)
	f.setDisplayName(t, f.owner, "Olive Owner")
	return &ownerCommentFixture{
		ownerBoundFixture: f,
		msgs:              agentmsg.NewService(agentmsg.NewPostgresStore(f.database), agentmsg.Config{}, slog.DiscardHandler),
	}
}

func (f *ownerCommentFixture) delivered(t *testing.T, a *agent.Agent) []agentmsg.Message {
	t.Helper()
	got, err := f.msgs.Deliver(f.ctx, a.ID)
	require.NoError(t, err)
	return got
}

func (f *ownerCommentFixture) onlyComment(t *testing.T, id int64) ticket.Comment {
	t.Helper()
	got := f.comments(t, id)
	require.Len(t, got, 1)
	return got[0]
}

func TestAgentComment_AnOwnerBoundSessionSpeaksForItsOwner(t *testing.T) {
	for name, session := range map[string]func(t *testing.T, f *ownerCommentFixture) *agent.Agent{
		"console with a live full-access grant": func(t *testing.T, f *ownerCommentFixture) *agent.Agent {
			c := f.console(t, "my-console")
			f.grant(t, c, f.owner, "agent")
			return c
		},
		"console of the console kind with a live full-access grant": func(t *testing.T, f *ownerCommentFixture) *agent.Agent {
			c, err := f.agents.CreateKind(f.ctx, "kind-console", agent.KindConsole)
			require.NoError(t, err)
			f.grant(t, c, f.owner, "agent")
			return c
		},
		"conductor": func(t *testing.T, f *ownerCommentFixture) *agent.Agent {
			return f.register(t, f.owner, "my-conductor")
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newOwnerCommentFixture(t)
			who := session(t, f)
			held, worker := f.heldBy(t, "Being built")

			_, err := f.tickets.AgentComment(f.ctx, who.ID, held.ID, "use the other order")
			require.NoError(t, err)

			c := f.onlyComment(t, held.ID)
			require.NotNil(t, c.OnBehalfOfUserID)
			assert.Equal(t, f.owner.ID, *c.OnBehalfOfUserID)
			assert.Equal(t, who.ID, *c.AuthorAgentID)
			assert.Nil(t, c.AuthorUserID, "the author stays the agent: the record says who really wrote it")
			assert.Equal(t, "use the other order (by agent "+who.Name+" for Olive Owner)", c.Body)

			got := f.delivered(t, worker)
			require.Len(t, got, 1)
			assert.Equal(t, agentmsg.KindTicketComment, got[0].Kind)
			require.NotNil(t, got[0].AuthorUserID)
			assert.Equal(t, f.owner.ID, *got[0].AuthorUserID)
			require.NotNil(t, got[0].TicketID)
			assert.Equal(t, held.ID, *got[0].TicketID)
			assert.Equal(t, who.Name, got[0].ViaAgentName)
			assert.Equal(t, c.Body, got[0].Body)
			rendered := agentmsg.Render(got)
			assert.Contains(t, rendered, "MESSAGE FROM THE HUMAN")
			assert.Contains(t, rendered, "(agent "+who.Name+", for Olive Owner, commented on ticket #")
		})
	}
}

func TestAgentComment_APlainWorkerIsNeverDelivered(t *testing.T) {
	f := newOwnerCommentFixture(t)
	held, worker := f.heldBy(t, "Someone else's")
	other, err := f.agents.Create(f.ctx, "other-worker")
	require.NoError(t, err)
	// A live grant does not make a non-console agent speak for anyone.
	f.grant(t, other, f.owner, "agent")

	_, err = f.tickets.AgentComment(f.ctx, other.ID, held.ID, "do it differently")
	require.NoError(t, err)

	c := f.onlyComment(t, held.ID)
	assert.Nil(t, c.OnBehalfOfUserID)
	assert.Equal(t, "do it differently", c.Body)
	assert.Empty(t, f.delivered(t, worker))
}

func TestAgentComment_ARevokedGrantPostsAPlainCommentThatIsNotDelivered(t *testing.T) {
	f := newOwnerCommentFixture(t)
	c := f.console(t, "my-console")
	f.revoke(t, f.grant(t, c, f.owner, "agent"))
	held, worker := f.heldBy(t, "Being built")

	_, err := f.tickets.AgentComment(f.ctx, c.ID, held.ID, "still posts")
	require.NoError(t, err, "a comment is never refused for want of an owner")

	got := f.onlyComment(t, held.ID)
	assert.Nil(t, got.OnBehalfOfUserID)
	assert.Equal(t, "still posts", got.Body)
	assert.Empty(t, f.delivered(t, worker))
}

func TestAgentComment_NotToItsOwnHolding(t *testing.T) {
	f := newOwnerCommentFixture(t)
	c := f.register(t, f.owner, "my-conductor")
	mine := f.ticket(t, "Mine")
	_, err := f.tickets.Claim(f.ctx, c.ID, &mine.ID)
	require.NoError(t, err)

	_, err = f.tickets.AgentComment(f.ctx, c.ID, mine.ID, "note to self")
	require.NoError(t, err)

	got := f.onlyComment(t, mine.ID)
	require.NotNil(t, got.OnBehalfOfUserID, "still recorded as the owner's")
	assert.Empty(t, f.delivered(t, c))
}

func TestAgentComment_OnATicketNobodyHoldsGoesNowhere(t *testing.T) {
	f := newOwnerCommentFixture(t)
	c := f.register(t, f.owner, "my-conductor")
	tk := f.ticket(t, "Unheld")

	_, err := f.tickets.AgentComment(f.ctx, c.ID, tk.ID, "anyone?")
	require.NoError(t, err)

	f.onlyComment(t, tk.ID)
	var n int
	require.NoError(t, f.database.QueryRow(f.ctx, `SELECT count(*) FROM agent_messages`).Scan(&n))
	assert.Zero(t, n)
}

func TestAgentComment_PastTheCapTheCopyIsDroppedAndTheCommentKept(t *testing.T) {
	f := newOwnerCommentFixture(t)
	c := f.register(t, f.owner, "my-conductor")
	held, worker := f.heldBy(t, "Being built")
	for range agentmsg.DefaultMaxPending {
		_, err := f.msgs.Send(f.ctx, &authn.User{ID: f.owner.ID}, worker.ID, "queued")
		require.NoError(t, err)
	}

	_, err := f.tickets.AgentComment(f.ctx, c.ID, held.ID, "too late")
	require.NoError(t, err)

	assert.Equal(t, "too late (by agent my-conductor for Olive Owner)", f.onlyComment(t, held.ID).Body)
	got := f.delivered(t, worker)
	require.Len(t, got, agentmsg.DefaultMaxPending)
	for _, m := range got {
		assert.Equal(t, "queued", m.Body)
	}
}

func TestAgentComment_OtherAgentCommentPathsStayPlain(t *testing.T) {
	f := newOwnerCommentFixture(t)
	c := f.register(t, f.owner, "my-conductor")
	held, worker := f.heldBy(t, "Being built")

	_, err := f.tickets.AddComment(f.ctx, held.ID, ticket.AgentAuthor(c.ID), "via AddComment")
	require.NoError(t, err)

	assert.Nil(t, f.onlyComment(t, held.ID).OnBehalfOfUserID)
	assert.Empty(t, f.delivered(t, worker))
}
