package llmusage_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
)

// conductor registers a conductor in the context's tenant the way the schema
// requires -- owned by a member -- and returns it as the credential's
// identity, which is how the usage hook learns what is calling.
func (w *wire) conductor(t *testing.T, ctx context.Context, name string) *agent.Agent {
	t.Helper()
	tenant, err := db.TenantFrom(ctx)
	require.NoError(t, err)
	owner := dbtest.NewUser(t, w.database)
	_, err = w.database.Global().Exec(context.Background(),
		`INSERT INTO global_tenant_memberships (id, tenant, user_id, role) VALUES ($1, $2, $3, 'member')`, uuid.New(), tenant, owner)
	require.NoError(t, err)
	id := uuid.New()
	_, err = w.database.Exec(ctx, `INSERT INTO agents (id, name, kind, owner_user_id) VALUES ($1, $2, 'conductor', $3)`, id, name, owner)
	require.NoError(t, err)
	got, err := w.agents.Get(ctx, id)
	require.NoError(t, err)
	require.True(t, got.IsConductor())
	return got
}

func (w *wire) conversation(t *testing.T, ctx context.Context, c *agent.Agent) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := w.database.Exec(ctx, `
		INSERT INTO conductor_conversations (id, conductor_id, owner_user_id, state)
		VALUES ($1, $2, $3, 'ready')`, id, c.ID, *c.OwnerUserID)
	require.NoError(t, err)
	return id
}

// conversationReport is a pi report for a conversation: the harness's session id is the
// conversation's, and so is the transcript.
func (w *wire) conversationReport(conversation uuid.UUID, requests int64) string {
	id := conversation.String()
	return fmt.Sprintf(`{"transcript_key":%q,"harness":"pi","session_id":%q,"scope":"main","conversation_id":%q,"records":%s}`,
		id, id, id, mustJSON([]any{piRecord(requests)}))
}

func (w *wire) usageRows(t *testing.T, ctx context.Context) []struct {
	Scope          string
	TicketID       *int64
	ConversationID *uuid.UUID
} {
	t.Helper()
	rows, err := w.database.Query(ctx, `SELECT scope, ticket_id, conversation_id FROM llm_usage ORDER BY transcript_key`)
	require.NoError(t, err)
	defer rows.Close()
	var out []struct {
		Scope          string
		TicketID       *int64
		ConversationID *uuid.UUID
	}
	for rows.Next() {
		var r struct {
			Scope          string
			TicketID       *int64
			ConversationID *uuid.UUID
		}
		require.NoError(t, rows.Scan(&r.Scope, &r.TicketID, &r.ConversationID))
		out = append(out, r)
	}
	require.NoError(t, rows.Err())
	return out
}

func TestAConversationIsOverheadAndTheRowSaysWhichConversation(t *testing.T) {
	w := newWire(t, 100)
	c := w.conductor(t, w.ctx, "c1")
	w.extra["conductor"] = c
	conv := w.conversation(t, w.ctx, c)

	rec := w.post(t, "conductor", w.conversationReport(conv, 2))

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.JSONEq(t, `{"scope":"overhead"}`, rec.Body.String())
	rows := w.usageRows(t, w.ctx)
	require.Len(t, rows, 1)
	assert.Equal(t, &conv, rows[0].ConversationID)
}

// Each turn's report is the conversation's full totals again.
func TestRepeatedConversationReportsCountOnceAndGrowByTheDifference(t *testing.T) {
	w := newWire(t, 100)
	c := w.conductor(t, w.ctx, "c1")
	w.extra["conductor"] = c
	conv := w.conversation(t, w.ctx, c)

	require.Equal(t, http.StatusOK, w.post(t, "conductor", w.conversationReport(conv, 2)).Code)
	require.Equal(t, http.StatusOK, w.post(t, "conductor", w.conversationReport(conv, 2)).Code)
	assert.Equal(t, 2.0, w.counter(t, "llm_requests_total"), "a turn reported again is not counted again")
	require.Equal(t, http.StatusOK, w.post(t, "conductor", w.conversationReport(conv, 5)).Code)

	assert.Equal(t, 5.0, w.counter(t, "llm_requests_total"))
	assert.Len(t, w.usageRows(t, w.ctx), 1)
}

func TestAConductorCannotChargeAnotherConductorsConversation(t *testing.T) {
	w := newWire(t, 100)
	mine, theirs := w.conductor(t, w.ctx, "mine"), w.conductor(t, w.ctx, "theirs")
	w.extra["mine"] = mine
	conv := w.conversation(t, w.ctx, theirs)

	rec := w.post(t, "mine", w.conversationReport(conv, 2))

	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	assert.Empty(t, w.usageRows(t, w.ctx))
	assert.Zero(t, w.counter(t, "llm_requests_total"), "and nothing was counted")
}

func TestAWorkerCannotNameAConversation(t *testing.T) {
	w := newWire(t, 100)
	c := w.conductor(t, w.ctx, "c1")
	conv := w.conversation(t, w.ctx, c)

	rec := w.post(t, builderToken, w.conversationReport(conv, 2))

	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	assert.Empty(t, w.usageRows(t, w.ctx))
}

func TestAnotherTenantsConversationIsNotChargeable(t *testing.T) {
	w := newWire(t, 100)
	mine := w.conductor(t, w.ctx, "mine")
	w.extra["mine"] = mine
	theirs := w.conductor(t, w.otherCtx, "theirs")
	conv := w.conversation(t, w.otherCtx, theirs)

	rec := w.post(t, "mine", w.conversationReport(conv, 2))

	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	assert.Empty(t, w.usageRows(t, w.otherCtx))
}

// A conductor's usage is the host's to report per conversation. An untagged
// report -- the Stop hook of a full conductor's session, say -- would be
// charged to nothing and count the same turns again.
func TestAConductorsUntaggedReportIsRefused(t *testing.T) {
	w := newWire(t, 100)
	c := w.conductor(t, w.ctx, "c1")
	w.extra["conductor"] = c

	rec := w.post(t, "conductor", piReport("pi-session", piRecord(2)).json(t))

	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	assert.Empty(t, w.usageRows(t, w.ctx))
	assert.Zero(t, w.counter(t, "llm_requests_total"))
}

func TestAConversationReportMustKeyItsTranscriptByTheConversation(t *testing.T) {
	w := newWire(t, 100)
	c := w.conductor(t, w.ctx, "c1")
	w.extra["conductor"] = c
	conv := w.conversation(t, w.ctx, c)
	id := conv.String()
	records := mustJSON([]any{piRecord(2)})

	for name, body := range map[string]string{
		"another session": fmt.Sprintf(`{"transcript_key":%q,"harness":"pi","session_id":"someone-else","scope":"main","conversation_id":%q,"records":%s}`, id, id, records),
		"another key":     fmt.Sprintf(`{"transcript_key":"other","harness":"pi","session_id":%q,"scope":"main","conversation_id":%q,"records":%s}`, id, id, records),
		"bad uuid":        fmt.Sprintf(`{"transcript_key":%q,"harness":"pi","session_id":%q,"scope":"main","conversation_id":"nope","records":%s}`, id, id, records),
	} {
		assert.Equal(t, http.StatusUnprocessableEntity, w.post(t, "conductor", body).Code, name)
	}
	assert.Empty(t, w.usageRows(t, w.ctx))

	sub := fmt.Sprintf(`{"transcript_key":%q,"harness":"pi","session_id":%q,"scope":"subagent:explore","conversation_id":%q,"records":%s}`, id+":a1", id, id, records)
	assert.Equal(t, http.StatusOK, w.post(t, "conductor", sub).Code, "a subagent's transcript within the conversation is the conversation's")
}

func TestUsageOutlivesItsConversation(t *testing.T) {
	w := newWire(t, 100)
	c := w.conductor(t, w.ctx, "c1")
	w.extra["conductor"] = c
	conv := w.conversation(t, w.ctx, c)
	require.Equal(t, http.StatusOK, w.post(t, "conductor", w.conversationReport(conv, 2)).Code)

	_, err := w.database.Exec(w.ctx, `DELETE FROM conductor_conversations WHERE id = $1`, conv)
	require.NoError(t, err)

	rows := w.usageRows(t, w.ctx)
	require.Len(t, rows, 1, "the usage stays")
	assert.Equal(t, "overhead", rows[0].Scope)
	assert.Nil(t, rows[0].ConversationID)
}

func mustJSON(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(raw)
}
