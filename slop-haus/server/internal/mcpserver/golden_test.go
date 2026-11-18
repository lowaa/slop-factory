package mcpserver_test

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/attachment"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/manifest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/mcpserver"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// Golden tests pin what an agent actually reads -- every byte of the text and
// the structured output -- for the tools whose answers carry the most
// guidance. They exist so the code behind them can move without the agents
// noticing: a change here is a change to every agent's prompt, and should be
// made on purpose, by regenerating with -update and reading the diff.

var updateGolden = flag.Bool("update", false, "rewrite the golden files from the current output")

// Fixed ids, so links and fallback names in the golden files are stable.
var (
	goldenTenant  = uuid.MustParse("00000000-0000-0000-0000-00000000000a")
	goldenCaller  = uuid.MustParse("00000000-0000-0000-0000-000000000001")
	goldenCritic  = uuid.MustParse("00000000-0000-0000-0000-000000000002")
	goldenUnknown = uuid.MustParse("00000000-0000-0000-0000-000000000003")
	goldenHuman   = uuid.MustParse("00000000-0000-0000-0000-000000000004")
	goldenSource  = uuid.MustParse("00000000-0000-0000-0000-000000000005")
	goldenAt      = time.Date(2026, 9, 26, 19, 2, 37, 0, time.FixedZone("AEST", 10*60*60))
	goldenPR      = "https://github.example/acme/repo/pull/12"
)

// goldenBoundary is the fence of external content in golden files; in
// production it is random per block.
const goldenBoundary = "0f1e2d3c4b5a69788796a5b4c3d2e1f0"

// goldenAgents knows two agents by name; any other lookup fails, as it
// would for an agent since deleted.
type goldenAgents struct{}

func (goldenAgents) Name(_ context.Context, id uuid.UUID) (string, error) {
	switch id {
	case goldenCaller:
		return "builder-1", nil
	case goldenCritic:
		return "critic-2", nil
	}
	return "", errors.New("no such agent")
}

// newGoldenRig is the tool surface over the real transport against the
// fake ticket service, with fixed ids throughout.
func newGoldenRig(t *testing.T) (*mcp.ClientSession, *fakeTickets) {
	t.Helper()
	tickets := &fakeTickets{}
	handler := mcpserver.NewHTTPHandler(mcpserver.Deps{
		Tickets: tickets, Sessions: &fakeSessions{}, Agents: goldenAgents{}, Roles: &fakeRoles{},
		BoardURL: testBoardURL, Changes: &fakeChanges{}, Manifests: noManifests{}, Attachments: noAttachments{},
		Boundary: func() string { return goldenBoundary },
	}, func(_ context.Context, token string) (authn.Identity, error) {
		if token != goodToken {
			return nil, errors.New("unknown token")
		}
		return tenantIdentity{id: goldenCaller, tenant: goldenTenant}, nil
	}, resourceMetadataURL, slog.New(slog.DiscardHandler))
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0.0.1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: srv.URL, HTTPClient: &http.Client{Transport: bearer{token: goodToken}},
	}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })
	return session, tickets
}

// noManifests is the tenant declaring none, which is all the tool list needs.
type noManifests struct{}

func (noManifests) Get(context.Context) (*manifest.Manifest, error) { return nil, nil }

func (noManifests) Revision(context.Context) (int64, error) { return 0, nil }

// assertGolden compares got with testdata/golden/<name>.golden, or rewrites
// the file under -update.
func assertGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "golden", name+".golden")
	if *updateGolden {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(got), 0o644))
		return
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "no golden file; run with -update to create it")
	assert.Equal(t, string(want), got, "%s differs from the golden file", name)
}

// renderResult is a tool result as the golden files hold it: the text the
// model reads, then the structured output.
func renderResult(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	structured, err := json.MarshalIndent(res.StructuredContent, "", "  ")
	require.NoError(t, err)
	return fmt.Sprintf("-- error: %t --\n-- text --\n%s\n-- structured --\n%s\n", res.IsError, resultText(t, res), structured)
}

func goldenPtr[T any](v T) *T { return &v }

// goldenTickets are the states a ticket is read or claimed in, each built
// on a plain one.
func goldenTickets() map[string]ticket.Ticket {
	plain := func() ticket.Ticket {
		return ticket.Ticket{
			ID: 7, Title: "Split the register", Body: "Move each tool next to its handler.\nNo behaviour change.",
			Status: ticket.StatusOpen, Priority: model.PriorityNormal, Revision: 0,
			Paths: []model.Path{{Path: "server/internal/mcpserver/"}, {Path: "server/internal/ticket/"}},
		}
	}
	with := func(edit func(*ticket.Ticket)) ticket.Ticket {
		t := plain()
		edit(&t)
		return t
	}
	at := goldenAt
	return map[string]ticket.Ticket{
		"plain":            plain(),
		"no_paths":         with(func(t *ticket.Ticket) { t.Paths = nil; t.Body = "" }),
		"needs_discussion": with(func(t *ticket.Ticket) { t.NeedsDiscussion = true }),
		"parked":           with(func(t *ticket.Ticket) { t.Status = ticket.StatusParked }),
		"requires_approval": with(func(t *ticket.Ticket) {
			t.RequiresApproval = true
		}),
		"in_review": with(func(t *ticket.Ticket) {
			t.Status, t.RequiresApproval, t.PRURL = ticket.StatusReview, true, goldenPtr(goldenPR)
		}),
		// The store clears both outcomes on going into review, but the
		// order the guidance is decided in says review wins; pinned here.
		"in_review_with_stale_outcome": with(func(t *ticket.Ticket) {
			t.Status, t.RequiresApproval, t.PRURL, t.ApprovedAt = ticket.StatusReview, true, goldenPtr(goldenPR), &at
		}),
		"approved": with(func(t *ticket.Ticket) {
			t.RequiresApproval, t.PRURL, t.ApprovedAt = true, goldenPtr(goldenPR), &at
		}),
		"approved_without_pr": with(func(t *ticket.Ticket) {
			t.RequiresApproval, t.ApprovedAt = true, &at
		}),
		"changes_requested": with(func(t *ticket.Ticket) {
			t.RequiresApproval, t.PRURL, t.ChangesRequestedAt = true, goldenPtr(goldenPR), &at
		}),
		// A review outcome is guidance even on a ticket that no longer
		// requires approval.
		"changes_requested_ungated": with(func(t *ticket.Ticket) {
			t.PRURL, t.ChangesRequestedAt = goldenPtr(goldenPR), &at
		}),
		"needs_discussion_approved": with(func(t *ticket.Ticket) {
			t.NeedsDiscussion, t.RequiresApproval, t.PRURL, t.ApprovedAt = true, true, goldenPtr(goldenPR), &at
		}),
		"needs_discussion_changes_requested": with(func(t *ticket.Ticket) {
			t.NeedsDiscussion, t.RequiresApproval, t.PRURL, t.ChangesRequestedAt = true, true, goldenPtr(goldenPR), &at
		}),
		"needs_discussion_in_review": with(func(t *ticket.Ticket) {
			t.NeedsDiscussion, t.Status, t.RequiresApproval, t.PRURL = true, ticket.StatusReview, true, goldenPtr(goldenPR)
		}),
		"needs_discussion_requires_approval": with(func(t *ticket.Ticket) {
			t.NeedsDiscussion, t.RequiresApproval = true, true
		}),
		"done_requires_approval": with(func(t *ticket.Ticket) {
			t.Status, t.RequiresApproval, t.PRURL, t.ApprovedAt = ticket.StatusDone, true, goldenPtr(goldenPR), &at
		}),
		"cancelled_requires_approval": with(func(t *ticket.Ticket) {
			t.Status, t.RequiresApproval = ticket.StatusCancelled, true
		}),
		"parked_requires_approval": with(func(t *ticket.Ticket) {
			t.Status, t.RequiresApproval = ticket.StatusParked, true
		}),
		// A live shadow of a ticket done through another attempt: the attempt's
		// own status, and the note that stops its agent reading done as its own.
		"shadow_of_done": with(func(t *ticket.Ticket) {
			t.Status, t.DoneThroughOther = ticket.StatusInProgress, true
		}),
		"blockers_and_roles": with(func(t *ticket.Ticket) {
			t.Status, t.Priority, t.Revision = ticket.StatusBlocked, model.PriorityHigh, 3
			t.DependsOn = []int64{3, 5}
			t.Blockers = []model.TicketRef{
				{ID: 3, Title: "Reorganise the store", Status: ticket.StatusDone},
				{ID: 5, Title: "Held-lease helper", Status: ticket.StatusInProgress},
			}
			t.RequiredRoles = []model.RoleRef{{ID: uuid.New(), Name: "deployer"}, {ID: uuid.New(), Name: "reviewer"}}
			t.RoleMatch = model.RoleMatchAll
		}),
		"external_source": with(func(t *ticket.Ticket) {
			t.Title = "[helpdesk] external report"
			t.Body = "Filed by intake source \"helpdesk\" (untrusted)."
			t.Status = ticket.StatusInProgress
			t.Intake = &model.IntakeRef{SourceID: goldenSource, Key: "zd-48213"}
		}),
		"any_role": with(func(t *ticket.Ticket) {
			t.RequiredRoles = []model.RoleRef{{ID: uuid.New(), Name: "deployer"}, {ID: uuid.New(), Name: "reviewer"}}
			t.RoleMatch = model.RoleMatchAny
		}),
	}
}

func TestGolden_TicketGet(t *testing.T) {
	for name, tc := range goldenTickets() {
		t.Run(name, func(t *testing.T) {
			session, tickets := newGoldenRig(t)
			tickets.got = &tc
			assertGolden(t, "ticket_get/"+name, renderResult(t, callGolden(t, session, "ticket_get", map[string]any{"ticket_id": tc.ID})))
		})
	}
}

// history is everything a resuming agent reads below the ticket itself:
// comments from a human and from agents, edits, and handoffs -- with an
// author whose name can't be looked up, who is shown by id.
func TestGolden_TicketGet_History(t *testing.T) {
	session, tickets := newGoldenRig(t)
	got := goldenTickets()["blockers_and_roles"]
	got.Revision = 7
	tickets.got = &got
	tickets.comments = []ticket.Comment{
		{ID: 1, AuthorUserID: &goldenHuman, Body: "Please keep the wording.", CreatedAt: goldenAt},
		{ID: 2, AuthorAgentID: &goldenCaller, Body: "Plan: golden tests first.", CreatedAt: goldenAt},
		{ID: 3, AuthorAgentID: &goldenUnknown, Body: "I was here once.", CreatedAt: goldenAt},
		{ID: 4, AuthorAgentID: &goldenCritic, Body: "Looks right.", CreatedAt: goldenAt},
	}
	tickets.contexts = []ticket.AgentContext{
		{ID: 1, TicketID: 7, AgentID: goldenCritic, Body: "An older handoff nobody should see.", CreatedAt: goldenAt},
		{ID: 2, TicketID: 7, AgentID: goldenUnknown, Body: "Branch ticket-7-split pushed.\nTests pass.", CreatedAt: goldenAt},
	}
	tickets.revisions = goldenRevisions()
	assertGolden(t, "ticket_get/history", renderResult(t, callGolden(t, session, "ticket_get", map[string]any{"ticket_id": 7})))
}

// The agent holding an attempt reads the attempt's branch in ticket_get, so a
// worker handed only the ticket id can find it.
func TestGolden_TicketGet_TheHoldersBranch(t *testing.T) {
	session, tickets := newGoldenRig(t)
	got := goldenTickets()["plain"]
	tickets.got = &got
	tickets.held = &ticket.AttemptRef{TicketID: got.ID, Number: 2}
	tickets.branch = &ticket.AttemptBranch{Name: "ticket-7-a2", Managed: true, Ready: true}
	got.BaseCommit = goldenPtr("0123456789abcdef0123456789abcdef01234567")
	assertGolden(t, "ticket_get/held_attempt", renderResult(t, callGolden(t, session, "ticket_get", map[string]any{"ticket_id": got.ID})))
}

// With nothing set, the fake answers with whichever ticket was asked for, so
// this pins that ticket_get reads the ticket it was given.
func TestGolden_TicketGet_TheRequestedTicket(t *testing.T) {
	session, _ := newGoldenRig(t)
	assertGolden(t, "ticket_get/requested", renderResult(t, callGolden(t, session, "ticket_get", map[string]any{"ticket_id": 42})))
}

// goldenRevisions are more edits than ticket_get shows, by a human, a known
// agent and one whose name can't be looked up.
func goldenRevisions() []ticket.Revision {
	title := func(from, to string) (*string, *string) { return &from, &to }
	var revs []ticket.Revision
	for i := 1; i <= 7; i++ {
		r := ticket.Revision{ID: int64(i), TicketID: 7, Number: i, CreatedAt: goldenAt.Add(time.Duration(i) * time.Minute)}
		switch i % 3 {
		case 0:
			r.Editor = ticket.UserAuthor(goldenHuman)
		case 1:
			r.Editor = ticket.AgentAuthor(goldenCaller)
		default:
			r.Editor = ticket.AgentAuthor(goldenUnknown)
		}
		if i%2 == 1 {
			r.OldTitle, r.NewTitle = title(fmt.Sprintf("Title v%d", i-1), fmt.Sprintf("Title v%d", i))
		} else {
			r.OldBody = goldenPtr("keep\nold line " + fmt.Sprint(i) + "\nkeep too")
			r.NewBody = goldenPtr("keep\nnew line " + fmt.Sprint(i) + "\nadded\nkeep too")
		}
		revs = append(revs, r)
	}
	return revs
}

func TestGolden_TicketNext(t *testing.T) {
	for name, tc := range goldenTickets() {
		t.Run(name, func(t *testing.T) {
			session, tickets := newGoldenRig(t)
			granted := tc
			// A claim hands over an in-progress ticket; the terminal and
			// parked ones stay as they are, to pin that they carry no
			// guidance whatever the review fields say.
			if granted.Status == ticket.StatusOpen || granted.Status == ticket.StatusBlocked {
				granted.Status = ticket.StatusInProgress
			}
			tickets.claim = &ticket.ClaimResult{Ticket: &granted}
			assertGolden(t, "ticket_next/"+name, renderResult(t, callGolden(t, session, "ticket_next", nil)))
		})
	}
}

func TestGolden_TicketNext_UnpromotedAttempt(t *testing.T) {
	session, tickets := newGoldenRig(t)
	granted := goldenTickets()["plain"]
	granted.Status = ticket.StatusInProgress
	granted.BaseCommit = goldenPtr("0123456789abcdef0123456789abcdef01234567")
	tickets.claim = &ticket.ClaimResult{Ticket: &granted, Unpromoted: true, Attempt: &ticket.AttemptRef{TicketID: 7, Number: 2}}
	assertGolden(t, "ticket_next/unpromoted_attempt", renderResult(t, callGolden(t, session, "ticket_next", nil)))
}

func TestGolden_TicketNext_NoTicket(t *testing.T) {
	for name, claim := range map[string]*ticket.ClaimResult{
		"empty": {},
		"blocked": {Blocked: &ticket.Blocked{
			TicketID: 7, ConflictingPath: "server/internal/mcpserver/", ConflictingTicketID: 5,
		}},
	} {
		t.Run(name, func(t *testing.T) {
			session, tickets := newGoldenRig(t)
			tickets.claim = claim
			assertGolden(t, "ticket_next/"+name, renderResult(t, callGolden(t, session, "ticket_next", nil)))
		})
	}
}

// The whole tool surface as a client lists it: every name, description and
// schema. Moving a schema between files must not change a byte of it.
func TestGolden_ToolList(t *testing.T) {
	session, _ := newGoldenRig(t)
	tools, err := session.ListTools(context.Background(), nil)
	require.NoError(t, err)
	listed, err := json.MarshalIndent(tools.Tools, "", "  ")
	require.NoError(t, err)
	assertGolden(t, "tools_list", string(listed)+"\n")
}

func callGolden(t *testing.T, session *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	require.NoError(t, err)
	require.False(t, res.IsError, strings.TrimSpace(resultText(t, res)))
	return res
}

// noAttachments is a tenant with none, which is all the tool list needs.
type noAttachments struct{}

func (noAttachments) ListForTicket(context.Context, attachment.Caller, int64) ([]attachment.Attachment, error) {
	return nil, nil
}

func (noAttachments) Meta(context.Context, attachment.Caller, uuid.UUID) (attachment.Attachment, error) {
	return attachment.Attachment{}, attachment.ErrNotFound
}

func (noAttachments) Open(context.Context, attachment.Caller, uuid.UUID) (attachment.Attachment, io.ReadCloser, error) {
	return attachment.Attachment{}, nil, attachment.ErrNotFound
}

func (noAttachments) AgentUploadToTicket(context.Context, authn.Identity, int64, *attachment.Upload) (attachment.Attachment, error) {
	return attachment.Attachment{}, attachment.ErrNotFound
}
