package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
)

// What GitHub's webhook does to the ticket whose pull request it is about:
// a review given or a merge made there, as the receiver answers GitHub and
// as the board then shows the ticket.

const (
	reviewerGitHubID = 4001
	strangerGitHubID = 4002
)

// linkGitHub gives GitHub user id to a person, as signing in with GitHub
// does.
func (r *contractRig) linkGitHub(id int64, userID uuid.UUID) {
	r.t.Helper()
	// The providers a deploy registers at startup are not in a test's database.
	for _, q := range []string{
		`INSERT INTO global_auth_providers (name, issuer) VALUES ('github', 'https://github.com') ON CONFLICT DO NOTHING`,
	} {
		_, err := r.boot.appDB.Global().Exec(r.t.Context(), q)
		require.NoError(r.t, err)
	}
	_, err := r.boot.appDB.Global().Exec(r.t.Context(), `UPDATE global_users SET is_active = true WHERE id = $1`, userID)
	require.NoError(r.t, err)
	_, err = r.boot.appDB.Global().Exec(r.t.Context(),
		`INSERT INTO global_user_identities (provider, subject, user_id, login) VALUES ('github', $1, $2, $3)`,
		strconv.FormatInt(id, 10), userID, fmt.Sprintf("user-%d", id))
	require.NoError(r.t, err)
}

// webhookRig is a rig whose signed-in person has linked their GitHub
// account, as reviewerGitHubID.
func newWebhookRig(t *testing.T) *contractRig {
	t.Helper()
	rig := newContractRig(t)
	rig.linkGitHub(reviewerGitHubID, rig.user.ID)
	return rig
}

// inReview is a ticket whose change slop-haus opened and whose agent has
// handed it over for review.
func (r *contractRig) inReview() *changeFlow {
	r.t.Helper()
	f := r.gate(true)
	f.review()
	return f
}

// post is a delivery as GitHub sends it, with whatever the test changes.
type post struct {
	event string
	// id is X-GitHub-Delivery; a new one when empty.
	id string
	// body is encoded as JSON unless it is already bytes.
	body any
	// contentType replaces application/json.
	contentType string
	// secret replaces the App's.
	secret string
	// unsigned sends no signature at all.
	unsigned bool
	// noDelivery sends no X-GitHub-Delivery.
	noDelivery bool
}

// receipt is what the receiver answered GitHub with.
type receipt struct {
	code int
	text string
}

func (r *contractRig) send(p *post) *receipt {
	r.t.Helper()
	hook := r.webhook()
	body, ok := p.body.([]byte)
	if !ok {
		// Every App delivery names its installation, which is how it finds
		// its tenant.
		if payload, isObject := p.body.(jsonObject); isObject {
			payload["installation"] = jsonObject{"id": appInstallationID}
		}
		var err error
		body, err = json.Marshal(p.body)
		require.NoError(r.t, err)
	}
	secret := hook.secret
	if p.secret != "" {
		secret = p.secret
	}
	header := map[string]string{"X-GitHub-Event": p.event}
	if !p.noDelivery {
		header["X-GitHub-Delivery"] = p.id
		if p.id == "" {
			header["X-GitHub-Delivery"] = uuid.NewString()
		}
	}
	if !p.unsigned {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(body)
		header["X-Hub-Signature-256"] = "sha256=" + hex.EncodeToString(mac.Sum(nil))
	}
	if p.contentType != "" {
		header["Content-Type"] = p.contentType
	}
	resp := do(r.t, &request{method: http.MethodPost, url: hook.url, body: string(body), header: header})
	return &receipt{code: resp.code, text: resp.body}
}

// received is a delivery the receiver answered 200 to with text.
func (r *contractRig) received(p *post, text string) {
	r.t.Helper()
	got := r.send(p)
	assert.Equal(r.t, http.StatusOK, got.code, got.text)
	assert.Equal(r.t, text+"\n", got.text)
}

// receivedContaining is received for a receipt that has ids in it.
func (r *contractRig) receivedContaining(p *post, prefix string, parts ...string) {
	r.t.Helper()
	got := r.send(p)
	assert.Equal(r.t, http.StatusOK, got.code, got.text)
	assert.True(r.t, strings.HasPrefix(got.text, prefix), "receipt %q does not start %q", got.text, prefix)
	for _, part := range parts {
		assert.Contains(r.t, got.text, part)
	}
}

type reviewPayload struct {
	action    string
	state     string
	reviewer  int64
	submitted time.Time
	body      string
	link      string
}

// later is a time after the ticket's review began, as GitHub reports times:
// to the second.
func later() time.Time { return time.Now().Add(time.Minute).UTC().Truncate(time.Second) }

func reviewEvent(p reviewPayload) jsonObject {
	if p.action == "" {
		p.action = "submitted"
	}
	if p.link == "" {
		p.link = flowLink
	}
	if p.reviewer == 0 {
		p.reviewer = reviewerGitHubID
	}
	if p.submitted.IsZero() {
		p.submitted = later()
	}
	return jsonObject{
		"action": p.action,
		"review": jsonObject{
			"state": p.state, "body": p.body, "html_url": p.link + "#pullrequestreview-9",
			"submitted_at": p.submitted.Format(time.RFC3339), "user": jsonObject{"id": p.reviewer, "login": fmt.Sprintf("user-%d", p.reviewer)},
		},
		"pull_request": jsonObject{"html_url": p.link},
	}
}

func prEvent(action string, merged bool, by int64, link string) jsonObject {
	if link == "" {
		link = flowLink
	}
	pr := jsonObject{"html_url": link, "merged": merged}
	if by != 0 {
		pr["merged_by"] = jsonObject{"id": by, "login": fmt.Sprintf("user-%d", by)}
	}
	return jsonObject{"action": action, "pull_request": pr}
}

func TestContractWebhook_Review(t *testing.T) {
	t.Parallel()
	t.Run("an approval by a member who linked GitHub moves the ticket as approving on the board does", func(t *testing.T) {
		rig := newWebhookRig(t)
		f := rig.inReview()

		rig.received(&post{event: "pull_request_review", body: reviewEvent(reviewPayload{state: "approved"})}, "applied: #1 approved")

		got := f.ticket()
		assert.Equal(t, "open", got["status"])
		assert.NotNil(t, got["approved_at"])
		assert.Equal(t, rig.user.ID.String(), got["approved_by_user_id"], "the approval is the member's")
		assert.Nil(t, got["changes_requested_at"])

		// What follows is the board's approval all over: the author has first
		// claim, is told to merge, and the gate lets it.
		other := rig.worker()
		assert.Zero(t, grantedID(other.mustCall("ticket_next", nil)))
		claimed := f.agent.mustCall("ticket_next", nil)
		assert.Equal(t, "approved", claimed.structured["ticket"].(map[string]any)["review"])
		f.agent.mustCall("integration_lock", nil)
		f.merges(jsonObject{"head_sha": headA})
	})

	t.Run("a review asking for changes sends it back with the reviewer's words and a link to the review", func(t *testing.T) {
		rig := newWebhookRig(t)
		f := rig.inReview()

		rig.received(&post{event: "pull_request_review", body: reviewEvent(reviewPayload{state: "changes_requested", body: "Please add a test."})},
			"applied: #1 sent back for changes")

		got := f.ticket()
		assert.Equal(t, "open", got["status"])
		assert.NotNil(t, got["changes_requested_at"])
		assert.Nil(t, got["approved_at"])
		bodies := commentBodies(rig.comments(f.id))
		assert.Equal(t, "Please add a test.\n\nChanges requested on GitHub: "+flowLink+"#pullrequestreview-9", bodies[len(bodies)-1])
		claimed := f.agent.mustCall("ticket_next", nil)
		assert.Equal(t, "changes_requested", claimed.structured["ticket"].(map[string]any)["review"])
	})

	t.Run("a review with only inline comments still carries the link", func(t *testing.T) {
		rig := newWebhookRig(t)
		f := rig.inReview()
		rig.received(&post{event: "pull_request_review", body: reviewEvent(reviewPayload{state: "changes_requested"})}, "applied: #1 sent back for changes")
		bodies := commentBodies(rig.comments(f.id))
		assert.Equal(t, "Changes requested on GitHub: "+flowLink+"#pullrequestreview-9", bodies[len(bodies)-1])
	})

	t.Run("a reviewer who is nobody here is ignored", func(t *testing.T) {
		rig := newWebhookRig(t)
		f := rig.inReview()
		before := commentBodies(rig.comments(f.id))

		stranger := reviewPayload{state: "approved", reviewer: strangerGitHubID}
		rig.received(&post{event: "pull_request_review", body: reviewEvent(stranger)},
			"ignored: the reviewer, GitHub user user-4002 (id 4002), is not a member here with that GitHub account linked")

		// Linked, but to someone who is not a member of this tenant.
		outsider := dbtest.NewUser(t, rig.boot.appDB)
		rig.linkGitHub(4003, outsider)
		rig.receivedContaining(&post{event: "pull_request_review", body: reviewEvent(reviewPayload{state: "changes_requested", reviewer: 4003, body: "No."})},
			"ignored: the reviewer, GitHub user user-4003 (id 4003), is not a member here")

		assert.Equal(t, "review", f.ticket()["status"])
		assert.Nil(t, f.ticket()["approved_at"])
		assert.Equal(t, before, commentBodies(rig.comments(f.id)), "nothing was written")
	})

	t.Run("a review made before the ticket's current review began is ignored", func(t *testing.T) {
		rig := newWebhookRig(t)
		f := rig.inReview()
		old := reviewPayload{state: "approved", submitted: time.Now().Add(-time.Hour).UTC().Truncate(time.Second)}
		rig.received(&post{event: "pull_request_review", body: reviewEvent(old)}, "ignored: the review predates the ticket's current review: #1")
		assert.Equal(t, "review", f.ticket()["status"])
		assert.Nil(t, f.ticket()["approved_at"])
	})

	t.Run("a review of a ticket no longer in review is ignored", func(t *testing.T) {
		rig := newWebhookRig(t)
		f := rig.inReview()
		rig.received(&post{event: "pull_request_review", body: reviewEvent(reviewPayload{state: "approved"})}, "applied: #1 approved")
		approvedAt := f.ticket()["approved_at"]

		rig.received(&post{event: "pull_request_review", body: reviewEvent(reviewPayload{state: "changes_requested", body: "Changed my mind."})},
			"ignored: ticket is not in review: #1 is open")
		assert.Equal(t, approvedAt, f.ticket()["approved_at"])
		assert.Nil(t, f.ticket()["changes_requested_at"])
	})

	t.Run("a review of a pull request no ticket has is ignored", func(t *testing.T) {
		rig := newWebhookRig(t)
		f := rig.inReview()
		other := reviewPayload{state: "approved", link: "https://github.com/acme/repo/pull/99"}
		rig.received(&post{event: "pull_request_review", body: reviewEvent(other)}, "ignored: no ticket for this PR: https://github.com/acme/repo/pull/99")
		assert.Equal(t, "review", f.ticket()["status"])
	})

	t.Run("a review that is not a verdict is ignored", func(t *testing.T) {
		rig := newWebhookRig(t)
		f := rig.inReview()
		for name, c := range map[string]struct {
			p    reviewPayload
			text string
		}{
			"a comment":              {reviewPayload{state: "commented"}, "ignored: a review that is commented changes nothing"},
			"a dismissal":            {reviewPayload{state: "dismissed"}, "ignored: a review that is dismissed changes nothing"},
			"an edit of an approval": {reviewPayload{state: "approved", action: "edited"}, "ignored: a review edited changes nothing"},
			"a dismissed approval":   {reviewPayload{state: "approved", action: "dismissed"}, "ignored: a review dismissed changes nothing"},
		} {
			rig.received(&post{event: "pull_request_review", body: reviewEvent(c.p)}, c.text)
			assert.Equal(t, "review", f.ticket()["status"], name)
		}
	})

	t.Run("a review with no reviewer is ignored", func(t *testing.T) {
		rig := newWebhookRig(t)
		f := rig.inReview()
		payload := reviewEvent(reviewPayload{state: "approved"})
		delete(payload["review"].(jsonObject), "user")
		rig.received(&post{event: "pull_request_review", body: payload}, "ignored: the review names no reviewer or submission time")
		assert.Equal(t, "review", f.ticket()["status"])
	})
}

func TestContractWebhook_Merged(t *testing.T) {
	t.Parallel()
	t.Run("a merge by a member closes the ticket in review", func(t *testing.T) {
		rig := newWebhookRig(t)
		f := rig.inReview()
		rig.received(&post{event: "pull_request", body: prEvent("closed", true, reviewerGitHubID, "")}, "applied: #1 closed as merged")

		got := f.ticket()
		assert.Equal(t, "done", got["status"])
		assert.NotNil(t, got["closed_at"])
		bodies := commentBodies(rig.comments(f.id))
		assert.Equal(t, "Merged on github.com: "+flowLink, bodies[len(bodies)-1])
		last := rig.comments(f.id)
		assert.Equal(t, rig.user.ID.String(), last[len(last)-1]["author_user_id"])
	})

	t.Run("a merge closes an approved ticket nobody has claimed again", func(t *testing.T) {
		rig := newWebhookRig(t)
		f := rig.inReview()
		rig.mustBoard(http.StatusOK, http.MethodPost, fmt.Sprintf("/api/tickets/%d/approve", f.id), nil)
		rig.received(&post{event: "pull_request", body: prEvent("closed", true, reviewerGitHubID, "")}, "applied: #1 closed as merged")
		assert.Equal(t, "done", f.ticket()["status"])
	})

	t.Run("a merge leaves a ticket an agent holds to the agent", func(t *testing.T) {
		rig := newWebhookRig(t)
		f := rig.inReview()
		rig.mustBoard(http.StatusOK, http.MethodPost, fmt.Sprintf("/api/tickets/%d/approve", f.id), nil)
		require.Equal(t, f.id, grantedID(f.agent.mustCall("ticket_next", nil)))

		rig.received(&post{event: "pull_request", body: prEvent("closed", true, reviewerGitHubID, "")},
			"ignored: ticket is not in review or approved and unclaimed: #1 is in_progress")
		assert.Equal(t, "in_progress", f.ticket()["status"])
		f.agent.mustComplete(jsonObject{"handoff": "Already merged on GitHub.", "pr_url": flowLink})
		assert.Equal(t, "done", f.ticket()["status"])
	})

	t.Run("a merge by someone who is nobody here is ignored", func(t *testing.T) {
		rig := newWebhookRig(t)
		f := rig.inReview()
		rig.received(&post{event: "pull_request", body: prEvent("closed", true, strangerGitHubID, "")},
			"ignored: the merger, GitHub user user-4002 (id 4002), is not a member here with that GitHub account linked")
		assert.Equal(t, "review", f.ticket()["status"])
	})

	t.Run("a merge of a pull request no ticket has is ignored", func(t *testing.T) {
		rig := newWebhookRig(t)
		f := rig.inReview()
		rig.received(&post{event: "pull_request", body: prEvent("closed", true, reviewerGitHubID, "https://github.com/acme/repo/pull/99")},
			"ignored: no ticket for this PR: https://github.com/acme/repo/pull/99")
		assert.Equal(t, "review", f.ticket()["status"])
	})

	t.Run("a ticket already done is not closed again", func(t *testing.T) {
		rig := newWebhookRig(t)
		f := rig.inReview()
		rig.received(&post{event: "pull_request", body: prEvent("closed", true, reviewerGitHubID, "")}, "applied: #1 closed as merged")
		rig.received(&post{event: "pull_request", body: prEvent("closed", true, reviewerGitHubID, "")}, "ignored: ticket is not in review or approved and unclaimed: #1 is done")
		assert.Len(t, rig.comments(f.id), 2, "the hand-over note and the merge note, and nothing for the repeat")
	})
}

func TestContractWebhook_EventsItDoesNotActOn(t *testing.T) {
	t.Parallel()
	rig := newWebhookRig(t)
	f := rig.inReview()
	before := commentBodies(rig.comments(f.id))

	for name, c := range map[string]struct {
		event string
		body  any
		text  string
	}{
		"a push to the branch":              {"push", jsonObject{"ref": "refs/heads/ticket-1-a1"}, `ignored: slop-haus does not act on "push" events`},
		"a pull request opened":             {"pull_request", prEvent("opened", false, 0, ""), "ignored: only a merged pull request changes anything"},
		"a pull request marked ready":       {"pull_request", prEvent("ready_for_review", false, 0, ""), "ignored: only a merged pull request changes anything"},
		"a pull request synchronized":       {"pull_request", prEvent("synchronize", false, 0, ""), "ignored: only a merged pull request changes anything"},
		"a pull request closed unmerged":    {"pull_request", prEvent("closed", false, reviewerGitHubID, ""), "ignored: only a merged pull request changes anything"},
		"a merged pull request, reopened":   {"pull_request", prEvent("reopened", true, reviewerGitHubID, ""), "ignored: only a merged pull request changes anything"},
		"a merge naming nobody":             {"pull_request", prEvent("closed", true, 0, ""), "ignored: the merge names nobody who merged it"},
		"a comment on a pull request":       {"issue_comment", jsonObject{"action": "created"}, `ignored: slop-haus does not act on "issue_comment" events`},
		"a review comment":                  {"pull_request_review_comment", jsonObject{"action": "created"}, `ignored: slop-haus does not act on "pull_request_review_comment" events`},
		"a payload that is not a review":    {"pull_request_review", jsonObject{"review": 7}, "ignored: malformed pull_request_review payload"},
		"a payload that is not a PR":        {"pull_request", jsonObject{"pull_request": 7}, "ignored: malformed pull_request payload"},
		"a link that is not a pull request": {"pull_request", prEvent("closed", true, reviewerGitHubID, "https://gitlab.com/acme/repo/-/merge_requests/1"), "ignored: not a github.com pull request"},
	} {
		rig.received(&post{event: c.event, body: c.body}, c.text)
		assert.Equal(t, "review", f.ticket()["status"], name)
	}
	assert.Equal(t, before, commentBodies(rig.comments(f.id)))

	t.Run("each is on the board's list of deliveries, with its outcome", func(t *testing.T) {
		deliveries := rig.deliveries()
		require.Len(t, deliveries, 12)
		for _, d := range deliveries {
			d := d.(map[string]any)
			assert.Equal(t, "ignored", d["outcome"])
			assert.Nil(t, d["ticket_id"])
			assert.NotEmpty(t, d["detail"])
		}
	})
}

func TestContractWebhook_Delivery(t *testing.T) {
	t.Parallel()
	t.Run("a ping is answered", func(t *testing.T) {
		rig := newWebhookRig(t)
		rig.received(&post{event: "ping", body: jsonObject{"zen": "Keep it logically awesome."}}, "applied: ping: GitHub reached this webhook")
	})

	t.Run("a delivery that cannot be trusted is refused and does nothing", func(t *testing.T) {
		rig := newWebhookRig(t)
		f := rig.inReview()
		approve := reviewEvent(reviewPayload{state: "approved"})

		for name, p := range map[string]*post{
			"signed with another secret": {event: "pull_request_review", body: approve, secret: "not-the-secret"},
			"unsigned":                   {event: "pull_request_review", body: approve, unsigned: true},
		} {
			got := rig.send(p)
			assert.Equal(t, http.StatusUnauthorized, got.code, name)
		}
		assert.Equal(t, "review", f.ticket()["status"])
		assert.Empty(t, rig.deliveries(), "a delivery that failed authentication is not recorded")
	})

	t.Run("a delivery with no id or no event is a bad request", func(t *testing.T) {
		rig := newWebhookRig(t)
		assert.Equal(t, http.StatusBadRequest, rig.send(&post{event: "ping", body: jsonObject{}, noDelivery: true}).code)
		assert.Equal(t, http.StatusBadRequest, rig.send(&post{event: "", body: jsonObject{}}).code)
	})

	t.Run("a content type other than JSON is ignored with what to change", func(t *testing.T) {
		rig := newWebhookRig(t)
		f := rig.inReview()
		got := rig.send(&post{event: "pull_request_review", body: reviewEvent(reviewPayload{state: "approved"}), contentType: "application/x-www-form-urlencoded"})
		assert.Equal(t, http.StatusOK, got.code)
		assert.Equal(t, "ignored: set the webhook's content type to application/json\n", got.text)
		assert.Equal(t, "review", f.ticket()["status"])

		withCharset := rig.send(&post{event: "ping", body: jsonObject{}, contentType: "application/json; charset=utf-8"})
		assert.Equal(t, "applied: ping: GitHub reached this webhook\n", withCharset.text)
	})

	t.Run("a delivery GitHub sends again is not acted on twice", func(t *testing.T) {
		rig := newWebhookRig(t)
		f := rig.inReview()
		const id = "11111111-2222-3333-4444-555555555555"
		sent := &post{id: id, event: "pull_request_review", body: reviewEvent(reviewPayload{state: "changes_requested", body: "Once."})}
		rig.received(sent, "applied: #1 sent back for changes")

		// The ticket comes back for another review, and the same delivery
		// arrives again: it would send it back a second time.
		require.Equal(t, f.id, grantedID(f.agent.mustCall("ticket_next", nil)))
		f.agent.mustCall("integration_lock", nil)
		f.review()
		sent.body = reviewEvent(reviewPayload{state: "changes_requested", body: "Once."})
		rig.received(sent, "duplicate: already handled")
		assert.Equal(t, "review", f.ticket()["status"])

		var seen int
		for _, d := range rig.deliveries() {
			if d.(map[string]any)["id"] == id {
				seen++
			}
		}
		assert.Equal(t, 1, seen, "the board lists it once")
	})

	t.Run("the board lists a delivery with what it did", func(t *testing.T) {
		rig := newWebhookRig(t)
		f := rig.inReview()
		rig.received(&post{event: "pull_request_review", body: reviewEvent(reviewPayload{state: "approved"})}, "applied: #1 approved")

		deliveries := rig.deliveries()
		require.Len(t, deliveries, 1)
		d := deliveries[0].(map[string]any)
		assert.Equal(t, "pull_request_review", d["event"])
		assert.Equal(t, "submitted", d["action"])
		assert.Equal(t, "applied", d["outcome"])
		assert.Equal(t, "#1 approved", d["detail"])
		assert.Equal(t, float64(f.id), d["ticket_id"])
	})

	t.Run("a delivery too large is refused", func(t *testing.T) {
		rig := newWebhookRig(t)
		got := rig.send(&post{event: "ping", body: []byte(`{"zen":"` + strings.Repeat("x", 1<<20) + `"}`)})
		assert.Equal(t, http.StatusRequestEntityTooLarge, got.code)
	})
}

func TestContractWebhook_AnArchivedTenantsDeliveryIsAcknowledgedAndDropped(t *testing.T) {
	t.Parallel()
	rig := newWebhookRig(t)
	f := rig.inReview()
	before := f.ticket()

	_, err := rig.boot.appDB.Global().Exec(t.Context(),
		`UPDATE global_tenants SET archived_at = now(), archived_by = $2 WHERE id = $1`, rig.user.TenantID, rig.user.ID)
	require.NoError(t, err)

	rig.received(&post{event: "pull_request_review", body: reviewEvent(reviewPayload{state: "approved"})}, "ignored: this workspace is archived")

	// The board is closed to an archived tenant, so read the ticket once it is back.
	_, err = rig.boot.appDB.Global().Exec(t.Context(), `UPDATE global_tenants SET archived_at = NULL, archived_by = NULL WHERE id = $1`, rig.user.TenantID)
	require.NoError(t, err)
	after := f.ticket()
	assert.Equal(t, before["status"], after["status"])
	assert.Nil(t, after["approved_at"], "the dropped approval did not touch the ticket")
}
