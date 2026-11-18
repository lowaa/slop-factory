package ticket_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// A follow-up filed from a replay is an ordinary new ticket: main's head, not
// the replay's base, and its reference is only text in the body.
func TestReplayFollowUp_IsFiledInPreparingWithoutTheReplaysBase(t *testing.T) {
	t.Parallel()
	r := newPinRig(t)
	tk := r.doneReplayable(t)
	added := r.addReplay(t, tk.ID)
	branch := ticket.BranchName(tk.ID, added.Number)
	body := fmt.Sprintf("Reference: PR https://example.test/pull/9, branch `%s`", branch)

	followUp, err := r.svc.Create(r.ctx, &ticket.CreateParams{
		Title: "Follow-up", Body: body, Status: ticket.StatusPreparing, CreatedByUserID: &r.human,
		Paths: []model.Path{{Path: "a/"}},
	})

	require.NoError(t, err)
	assert.Equal(t, ticket.StatusPreparing, followUp.Status)
	assert.Nil(t, followUp.BaseRef, "never based on the replay's base")
	assert.Nil(t, followUp.BaseCommit)
	assert.Contains(t, followUp.Body, branch)
	assert.Contains(t, followUp.Body, "https://example.test/pull/9")
	assert.Empty(t, r.host.TagCreates(), "no base tag is made for it")
	assert.Equal(t, ticket.StatusDone, r.status(t, tk.ID), "the original stays done")
}
