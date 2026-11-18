package dsr_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-privacy/dsr"
)

func (f *fixture) create(t *testing.T, typ dsr.Type) *dsr.Request {
	t.Helper()
	r, err := f.svc.Create(t.Context(), &dsr.NewRequest{
		RequesterName: " Pat ", RequesterEmail: "Pat@Example.TEST", Type: typ, Source: dsr.SourcePublicForm,
		Details: "please", Actor: "public",
	})
	require.NoError(t, err)
	return r
}

func kinds(evs []*dsr.Event) []dsr.EventKind {
	out := make([]dsr.EventKind, len(evs))
	for i, e := range evs {
		out[i] = e.Kind
	}
	return out
}

func TestLifecycleIsAuditedEndToEnd(t *testing.T) {
	ctx := t.Context()
	f := newFixture(t)
	r := f.create(t, dsr.TypeCCPAKnow)

	assert.Equal(t, "pat@example.test", r.RequesterEmail)
	assert.Equal(t, "Pat", r.RequesterName)
	assert.Equal(t, dsr.StatusReceived, r.Status)
	assert.True(t, f.clock.now.AddDate(0, 0, 45).Equal(r.DueAt), "CCPA: 45 days from receipt")

	f.clock.Advance(time.Hour)
	_, err := f.svc.SetStatus(ctx, r.ID, "alice", dsr.StatusVerifying, "emailed them")
	require.NoError(t, err)
	_, err = f.svc.Verify(ctx, r.ID, "alice", dsr.VerifiedEmailReply, "replied from the address")
	require.NoError(t, err)
	require.NoError(t, f.svc.AddNote(ctx, r.ID, "alice", "pulled their records"))
	extended, err := f.svc.Extend(ctx, r.ID, "alice", "large account")
	require.NoError(t, err)
	assert.True(t, f.clock.now.AddDate(0, 0, -0).Before(extended.DueAt))
	assert.True(t, r.ReceivedAt.AddDate(0, 0, 90).Equal(extended.DueAt), "extended from the received date, 45 + 45")
	closed, err := f.svc.SetStatus(ctx, r.ID, "alice", dsr.StatusCompleted, "sent")
	require.NoError(t, err)
	require.NotNil(t, closed.ClosedAt)

	got, evs, err := f.svc.Get(ctx, r.ID)
	require.NoError(t, err)
	assert.Equal(t, dsr.StatusCompleted, got.Status)
	assert.True(t, got.Verified)
	assert.Equal(t, dsr.VerifiedEmailReply, got.VerificationMethod)
	assert.Equal(t, []dsr.EventKind{dsr.EventReceived, dsr.EventStatus, dsr.EventVerified, dsr.EventNote, dsr.EventExtended, dsr.EventStatus}, kinds(evs))
	assert.Equal(t, "public", evs[0].Actor)
	assert.Equal(t, "alice", evs[1].Actor)
	assert.Equal(t, dsr.StatusReceived, evs[1].FromStatus)
	assert.Equal(t, dsr.StatusVerifying, evs[1].ToStatus)
	assert.Equal(t, dsr.StatusCompleted, evs[5].ToStatus)

	t.Run("a closed request takes no further change, but a note", func(t *testing.T) {
		_, err := f.svc.SetStatus(ctx, r.ID, "alice", dsr.StatusInProgress, "")
		assert.ErrorIs(t, err, dsr.ErrClosed)
		_, err = f.svc.Extend(ctx, r.ID, "alice", "again")
		assert.ErrorIs(t, err, dsr.ErrClosed)
		_, err = f.svc.Verify(ctx, r.ID, "alice", dsr.VerifiedOther, "")
		assert.ErrorIs(t, err, dsr.ErrClosed)
		require.NoError(t, f.svc.AddNote(ctx, r.ID, "alice", "requester complained"))
		_, evs, _ := f.svc.Get(ctx, r.ID)
		assert.Len(t, evs, 7)
	})
}

func TestRulesOfChange(t *testing.T) {
	ctx := t.Context()
	f := newFixture(t)

	t.Run("a deadline extends once", func(t *testing.T) {
		r := f.create(t, dsr.TypeAccess)
		_, err := f.svc.Extend(ctx, r.ID, "op", "complex")
		require.NoError(t, err)
		_, err = f.svc.Extend(ctx, r.ID, "op", "more")
		assert.ErrorIs(t, err, dsr.ErrAlreadyExtended)
		_, err = f.svc.Extend(ctx, f.create(t, dsr.TypeAccess).ID, "op", "  ")
		assert.ErrorIs(t, err, dsr.ErrInvalid, "a reason is required")
	})

	t.Run("data is not disclosed, changed or deleted for an unverified requester", func(t *testing.T) {
		for _, typ := range []dsr.Type{dsr.TypeAccess, dsr.TypeErasure, dsr.TypeCCPAKnow, dsr.TypeCCPADelete, dsr.TypeCCPACorrect, dsr.TypeRectification, dsr.TypePortability} {
			r := f.create(t, typ)
			_, err := f.svc.SetStatus(ctx, r.ID, "op", dsr.StatusCompleted, "")
			assert.ErrorIs(t, err, dsr.ErrNotVerified, typ)
			// Refusing is how an unverifiable request ends.
			_, err = f.svc.SetStatus(ctx, r.ID, "op", dsr.StatusRefused, "could not verify")
			assert.NoError(t, err, typ)
		}
		for _, typ := range []dsr.Type{dsr.TypeCCPAOptOut, dsr.TypeCCPALimit, dsr.TypeObjection, dsr.TypeRestriction} {
			r := f.create(t, typ)
			_, err := f.svc.SetStatus(ctx, r.ID, "op", dsr.StatusCompleted, "")
			assert.NoError(t, err, typ)
		}
	})

	t.Run("a status that is not a move is refused", func(t *testing.T) {
		r := f.create(t, dsr.TypeObjection)
		for _, to := range []dsr.Status{dsr.StatusReceived, "nonsense", dsr.StatusReceived} {
			_, err := f.svc.SetStatus(ctx, r.ID, "op", to, "")
			assert.ErrorIs(t, err, dsr.ErrInvalid, to)
		}
		_, err := f.svc.SetStatus(ctx, r.ID, "op", dsr.StatusInProgress, "")
		require.NoError(t, err)
		_, err = f.svc.SetStatus(ctx, r.ID, "op", dsr.StatusInProgress, "")
		assert.ErrorIs(t, err, dsr.ErrInvalid, "already there")
	})

	t.Run("an unknown request", func(t *testing.T) {
		_, err := f.svc.SetStatus(ctx, uuid.New(), "op", dsr.StatusInProgress, "")
		assert.ErrorIs(t, err, dsr.ErrNotFound)
		assert.ErrorIs(t, f.svc.AddNote(ctx, uuid.New(), "op", "x"), dsr.ErrNotFound)
	})
}

func TestCreateValidates(t *testing.T) {
	ctx := t.Context()
	f := newFixture(t)
	good := func() *dsr.NewRequest {
		return &dsr.NewRequest{RequesterEmail: "a@example.test", Type: dsr.TypeAccess, Source: dsr.SourceEmail, Actor: "op"}
	}
	cases := map[string]func(n *dsr.NewRequest){
		"unknown type":       func(n *dsr.NewRequest) { n.Type = "sell_my_data" },
		"unknown source":     func(n *dsr.NewRequest) { n.Source = "carrier_pigeon" },
		"no actor":           func(n *dsr.NewRequest) { n.Actor = "" },
		"no email":           func(n *dsr.NewRequest) { n.RequesterEmail = " " },
		"display name":       func(n *dsr.NewRequest) { n.RequesterEmail = "Pat <a@example.test>" },
		"two addresses":      func(n *dsr.NewRequest) { n.RequesterEmail = "a@example.test, b@example.test" },
		"long details":       func(n *dsr.NewRequest) { n.Details = strings.Repeat("x", 4001) },
		"long name":          func(n *dsr.NewRequest) { n.RequesterName = strings.Repeat("x", 201) },
		"NUL in details":     func(n *dsr.NewRequest) { n.Details = "a\x00b" },
		"received in future": func(n *dsr.NewRequest) { n.ReceivedAt = f.clock.now.Add(time.Hour) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			n := good()
			mutate(n)
			_, err := f.svc.Create(ctx, n)
			assert.ErrorIs(t, err, dsr.ErrInvalid)
		})
	}
	t.Run("errors name the field, not the value", func(t *testing.T) {
		n := good()
		n.RequesterEmail = "secret-person at bad"
		_, err := f.svc.Create(ctx, n)
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "secret-person")
	})
	t.Run("an operator can log an email that arrived earlier, and its clock starts then", func(t *testing.T) {
		n := good()
		n.ReceivedAt = f.clock.now.Add(-10 * 24 * time.Hour)
		r, err := f.svc.Create(ctx, n)
		require.NoError(t, err)
		assert.True(t, n.ReceivedAt.AddDate(0, 1, 0).Equal(r.DueAt))
	})
}

func TestDueStateAndAlerts(t *testing.T) {
	ctx := t.Context()
	f := newFixture(t)
	r := f.create(t, dsr.TypeCCPAKnow) // due in 45 days
	var sent [][]*dsr.Alert
	notify := func(_ context.Context, as []*dsr.Alert) error { sent = append(sent, as); return nil }
	check := func() int {
		n, err := f.svc.CheckAlerts(ctx, notify)
		require.NoError(t, err)
		return n
	}
	mine := func(as []*dsr.Alert) *dsr.Alert {
		for _, a := range as {
			if a.Request.ID == r.ID {
				return a
			}
		}
		return nil
	}

	assert.Equal(t, dsr.DueNone, f.svc.DueState(r))
	f.clock.Advance(40 * 24 * time.Hour) // 5 days left
	assert.Equal(t, dsr.DueSoon, f.svc.DueState(r))
	check()
	require.Len(t, sent, 1)
	require.NotNil(t, mine(sent[0]))
	assert.Equal(t, dsr.AlertDueSoon, mine(sent[0]).Kind)

	sent = nil
	check()
	assert.Empty(t, sent, "the same alert is not sent twice")

	f.clock.Advance(6 * 24 * time.Hour)
	assert.Equal(t, dsr.DuePastDue, f.svc.DueState(r))
	check()
	require.Len(t, sent, 1)
	assert.Equal(t, dsr.AlertOverdue, mine(sent[0]).Kind)

	_, evs, _ := f.svc.Get(ctx, r.ID)
	assert.Equal(t, []dsr.EventKind{dsr.EventReceived, dsr.EventAlertSoon, dsr.EventAlertLate}, kinds(evs))
	assert.Equal(t, dsr.SystemActor, evs[1].Actor)

	t.Run("a closed request is never due", func(t *testing.T) {
		_, err := f.svc.SetStatus(ctx, r.ID, "op", dsr.StatusRefused, "")
		require.NoError(t, err)
		got, _, _ := f.svc.Get(ctx, r.ID)
		assert.Equal(t, dsr.DueNone, f.svc.DueState(got))
	})

	t.Run("an extension starts the alerts afresh", func(t *testing.T) {
		g := newFixture(t)
		x := g.create(t, dsr.TypeAccess)
		g.clock.Advance(27 * 24 * time.Hour)
		n, err := g.svc.CheckAlerts(ctx, func(_ context.Context, as []*dsr.Alert) error { return nil })
		require.NoError(t, err)
		require.Positive(t, n)
		_, err = g.svc.Extend(ctx, x.ID, "op", "complex")
		require.NoError(t, err)
		got, _, _ := g.svc.Get(ctx, x.ID)
		assert.Nil(t, got.AlertedSoonAt)
		assert.Nil(t, got.AlertedOverdueAt)
	})

	t.Run("a failed send is offered again", func(t *testing.T) {
		g := newFixture(t)
		x := g.create(t, dsr.TypeAccess)
		g.clock.Advance(40 * 24 * time.Hour)
		boom := errors.New("smtp down")
		_, err := g.svc.CheckAlerts(ctx, func(context.Context, []*dsr.Alert) error { return boom })
		require.ErrorIs(t, err, boom)
		var got []*dsr.Alert
		_, err = g.svc.CheckAlerts(ctx, func(_ context.Context, as []*dsr.Alert) error { got = as; return nil })
		require.NoError(t, err)
		found := false
		for _, a := range got {
			found = found || a.Request.ID == x.ID
		}
		assert.True(t, found)
		_, evs, _ := g.svc.Get(ctx, x.ID)
		assert.Equal(t, []dsr.EventKind{dsr.EventReceived, dsr.EventAlertLate}, kinds(evs), "the failed attempt left no event")
	})
}
