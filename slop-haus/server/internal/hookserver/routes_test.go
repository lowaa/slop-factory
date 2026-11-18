package hookserver_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/humatest"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/hookserver"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/session"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// newRoutes mounts the hook routes the way the server does, with the caller
// already authenticated, so these exercise the request schema a real hook
// payload goes through -- which the handler-level tests skip.
func newRoutes(t *testing.T) (humatest.TestAPI, *harness) {
	t.Helper()
	h := newHarness()
	_, api := humatest.New(t)
	api.UseMiddleware(func(ctx huma.Context, next func(huma.Context)) {
		next(huma.WithContext(ctx, authn.NewContext(ctx.Context(), identity{id: h.agentID})))
	})
	hookserver.RegisterRoutes(api, h.handler)
	return api, h
}

func TestRoutes_RecordTheRunsSelfDescription(t *testing.T) {
	for _, path := range []string{"/hooks/stop", "/hooks/session-start"} {
		t.Run(path, func(t *testing.T) {
			api, h := newRoutes(t)

			resp := api.Post(path, map[string]any{
				"session_id": "s1",
				"cwd":        "/w",
				"slop_haus": map[string]any{
					"harness":         "pi",
					"harness_version": "0.87.0",
					"provider":        "anthropic",
					"model":           "claude-sonnet-4-5",
					"labels":          map[string]any{"gpu": "a100", "docker": ""},
				},
			})
			require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
			assert.Equal(t, &session.Description{
				Harness: "pi", HarnessVersion: "0.87.0", Provider: "anthropic", Model: "claude-sonnet-4-5",
				Labels: map[string]string{"gpu": "a100", "docker": ""},
			}, h.sessions.described["s1"])
		})
	}
}

// A description is decoration on a hook whose real job is the agent loop:
// whatever shape it arrives in, the hook still answers, and a Stop that
// came back 422 would let the agent quit.
func TestRoutes_AMalformedDescriptionNeverFailsTheHook(t *testing.T) {
	for _, bad := range []any{
		"not an object",
		42,
		[]any{"a", "b"},
		map[string]any{"harness": map[string]any{"nested": true}, "labels": "gpu"},
		map[string]any{"labels": map[string]any{"gpu": []any{1}}},
	} {
		for _, path := range []string{"/hooks/stop", "/hooks/session-start"} {
			api, _ := newRoutes(t)
			resp := api.Post(path, map[string]any{"session_id": "s1", "cwd": "/w", "slop_haus": bad})
			assert.Equal(t, http.StatusOK, resp.Code, "%s with %v: %s", path, bad, resp.Body.String())
		}
	}
}

func TestRoutes_NoDescriptionRecordsNothing(t *testing.T) {
	api, h := newRoutes(t)
	resp := api.Post("/hooks/stop", map[string]any{"session_id": "s1", "cwd": "/w"})
	require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
	assert.Empty(t, h.sessions.described)
}

// A superseded run keeps firing hooks; it must not relabel the live one.
func TestStop_ASupersededRunIsNotDescribed(t *testing.T) {
	h := newHarness()
	h.sessions.superseded = true
	_, err := h.handler.Stop(h.ctx, &hookserver.StopInput{
		SessionID:       "old",
		SelfDescription: hookserver.SelfDescription{SlopHaus: []byte(`{"harness":"pi"}`)},
	})
	require.NoError(t, err)
	assert.Empty(t, h.sessions.described)
}

// A run whose SessionStart never fired -- every Claude Code run -- is bound
// by its first Stop, and described by it too.
func TestStop_AnUnregisteredRunIsBoundAndDescribed(t *testing.T) {
	h := newHarness()
	h.sessions.unknownSession = true
	_, err := h.handler.Stop(h.ctx, &hookserver.StopInput{
		SessionID:       "new",
		SelfDescription: hookserver.SelfDescription{SlopHaus: []byte(`{"harness":"claude","model":"claude-opus-5-5"}`)},
	})
	require.NoError(t, err)
	assert.Equal(t, 1, h.sessions.startCalls)
	require.Contains(t, h.sessions.described, "new")
	assert.Equal(t, "claude-opus-5-5", h.sessions.described["new"].Model)
}

// Every Stop says what the run is now, so a model switched mid-run lands on
// the next one rather than the first answer sticking.
func TestStop_EachStopRefreshesTheDescription(t *testing.T) {
	h := newHarness()
	for _, model := range []string{"claude-sonnet-4-5", "claude-opus-5-5"} {
		_, err := h.handler.Stop(h.ctx, &hookserver.StopInput{
			SessionID:       "s1",
			SelfDescription: hookserver.SelfDescription{SlopHaus: []byte(`{"harness":"pi","model":"` + model + `"}`)},
		})
		require.NoError(t, err)
		assert.Equal(t, model, h.sessions.described["s1"].Model)
	}
}

// Recording the description is bookkeeping: failing at it must not change
// whether the agent is sent back to work.
func TestStop_AFailureToRecordTheDescriptionChangesNothing(t *testing.T) {
	h := newHarness()
	h.sessions.describeErr = errors.New("database is down")
	h.tickets.lease = &ticket.Lease{TicketID: 42}

	decision, err := h.handler.Stop(h.ctx, &hookserver.StopInput{
		SessionID:       "s1",
		SelfDescription: hookserver.SelfDescription{SlopHaus: []byte(`{"harness":"pi"}`)},
	})
	require.NoError(t, err)
	assert.Equal(t, "block", decision.Decision)

	_, err = h.handler.SessionStart(h.ctx, &hookserver.SessionStartInput{
		SessionID:       "s1",
		SelfDescription: hookserver.SelfDescription{SlopHaus: []byte(`{"harness":"pi"}`)},
	})
	require.NoError(t, err)
}

// What a hook payload says about how the session started, and which
// harness it is, has to survive the request schema to change anything.
func TestRoutes_SessionStartSourceAndHarnessReachTheHandler(t *testing.T) {
	t.Run("a session replaced in process is not a second run", func(t *testing.T) {
		for source, warns := range map[string]bool{"startup": true, "new": false, "clear": false} {
			api, h := newRoutes(t)
			h.sessions.startResult = &session.StartResult{
				Arrival: session.ArrivalNew, Session: &session.Session{}, ConcurrentRun: true,
			}
			resp := api.Post("/hooks/session-start", map[string]any{"session_id": "s2", "cwd": "/w", "source": source})
			require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
			assert.Equal(t, warns, strings.Contains(resp.Body.String(), "another agent run"), source)
		}
	})

}

// fakeLayouts records each layout the hooks save, by agent.
type fakeLayouts struct {
	saved map[uuid.UUID]*agent.Layout
	err   error
}

func (f *fakeLayouts) SaveLayout(_ context.Context, agentID uuid.UUID, layout *agent.Layout) error {
	if f.err != nil {
		return f.err
	}
	if f.saved == nil {
		f.saved = map[uuid.UUID]*agent.Layout{}
	}
	f.saved[agentID] = layout
	return nil
}

func newRoutesWithLayouts(t *testing.T) (humatest.TestAPI, *harness, *fakeLayouts) {
	t.Helper()
	layouts := &fakeLayouts{}
	h := newHarnessWith(func(d *hookserver.Deps) { d.Layouts = layouts })
	_, api := humatest.New(t)
	api.UseMiddleware(func(ctx huma.Context, next func(huma.Context)) {
		next(huma.WithContext(ctx, authn.NewContext(ctx.Context(), identity{id: h.agentID})))
	})
	hookserver.RegisterRoutes(api, h.handler)
	return api, h, layouts
}

var reportedLayout = map[string]any{
	"root":  map[string]any{"path": ".", "git": true, "origin": "https://user:tok@github.com/acme/root"},
	"repos": []any{map[string]any{"path": "app", "git": true, "origin": "github.com/acme/app"}},
}

func TestRoutes_TheWorkspaceLayoutIsRecordedPerAgent(t *testing.T) {
	for _, path := range []string{"/hooks/stop", "/hooks/session-start"} {
		t.Run(path, func(t *testing.T) {
			api, h, layouts := newRoutesWithLayouts(t)

			resp := api.Post(path, map[string]any{"session_id": "s1", "cwd": "/w", "slop_haus": map[string]any{"layout": reportedLayout}})
			require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())

			assert.Equal(t, &agent.Layout{
				Root:  agent.LayoutEntry{Path: ".", Git: true, Origin: "github.com/acme/root"},
				Repos: []agent.LayoutEntry{{Path: "app", Git: true, Origin: "github.com/acme/app"}},
			}, layouts.saved[h.agentID], "recorded for the calling agent, without the credential")
		})
	}
}

// A workspace that has not updated sends no layout. It is unknown, which
// blocks nothing: the hook answers exactly as it does for an agent that
// reports, and nothing is written.
func TestRoutes_NoLayoutIsUnknownAndBlocksNothing(t *testing.T) {
	for _, path := range []string{"/hooks/stop", "/hooks/session-start"} {
		t.Run(path, func(t *testing.T) {
			api, h, layouts := newRoutesWithLayouts(t)
			h.tickets.lease = &ticket.Lease{TicketID: 7}
			h.tickets.ticket = &ticket.Ticket{ID: 7, Status: ticket.StatusInProgress}

			resp := api.Post(path, map[string]any{"session_id": "s1", "cwd": "/w", "slop_haus": map[string]any{"harness": "claude"}})

			require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
			assert.Empty(t, layouts.saved)
			if path == "/hooks/stop" {
				assert.Contains(t, resp.Body.String(), `"decision":"block"`, "an agent holding a ticket is still sent back to it")
			}
		})
	}
}

// The report is bookkeeping on a hook whose real job is the agent loop: a
// store that fails, or a layout that makes no sense, changes nothing about
// the answer -- a Stop that failed would let the agent quit.
func TestRoutes_ALayoutThatCannotBeRecordedNeverFailsTheHook(t *testing.T) {
	api, h, layouts := newRoutesWithLayouts(t)
	h.tickets.lease = &ticket.Lease{TicketID: 7}
	h.tickets.ticket = &ticket.Ticket{ID: 7, Status: ticket.StatusInProgress}
	layouts.err = errors.New("db down")

	resp := api.Post("/hooks/stop", map[string]any{"session_id": "s1", "cwd": "/w", "slop_haus": map[string]any{"layout": reportedLayout}})
	require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
	assert.Contains(t, resp.Body.String(), `"decision":"block"`)

	layouts.err = nil
	for _, bad := range []any{"nope", 7, map[string]any{"root": "x"}, []any{1}} {
		resp := api.Post("/hooks/stop", map[string]any{"session_id": "s1", "cwd": "/w", "slop_haus": map[string]any{"layout": bad}})
		require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
	}
	assert.Empty(t, layouts.saved)
}

func TestRoutes_ASupersededSessionStartDoesNotOverwriteTheLayout(t *testing.T) {
	api, h, layouts := newRoutesWithLayouts(t)
	h.sessions.startResult = &session.StartResult{Arrival: session.ArrivalStale, Session: &session.Session{}}

	resp := api.Post("/hooks/session-start", map[string]any{
		"session_id": "old", "cwd": "/w", "slop_haus": map[string]any{"layout": reportedLayout},
	})

	require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
	assert.Empty(t, layouts.saved)
}
