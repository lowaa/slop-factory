package ticket_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

const aPR = "https://example.test/org/repo/pull/7"

// The link ends up as an href on the board, so anything a browser could
// follow somewhere other than an https page the human can read is refused.
func TestDone_RefusesAnInvalidPRURL(t *testing.T) {
	t.Parallel()
	// One database for every case: each claims and finishes a ticket of its
	// own, so none sees another's.
	f := newFixture(t)
	agentID := f.agent(t, "builder-1")
	for name, raw := range map[string]string{
		"http":             "http://example.test/pull/7",
		"javascript":       "javascript:alert(1)",
		"data":             "data:text/html,<script>alert(1)</script>",
		"uppercase scheme": "HTTPS://example.test/pull/7",
		"no host":          "https:///pull/7",
		"relative":         "/org/repo/pull/7",
		"credentials":      "https://user:secret@example.test/pull/7",
		"leading space":    " https://example.test/pull/7",
		"embedded newline": "https://example.test/pull/7\nhttps://evil.test",
		"tab":              "https://exam\tple.test/pull/7",
		// url.Parse accepts these two; only the whitespace check refuses them.
		"line separator":       "https://example.test/pull/7\u2028https://evil.test",
		"no-break space":       "https://example.test/pull/7\u00a0x",
		"protocol relative":    "//example.test/pull/7",
		"too long":             "https://example.test/" + strings.Repeat("a", 2048),
		"scheme without slash": "https:example.test/pull/7",
	} {
		t.Run(name, func(t *testing.T) {
			f.ticket(t, "fix it "+name)
			f.claim(t, agentID)

			_, err := f.svc.Done(f.ctx, agentID, &ticket.DoneParams{Summary: "merged", PRURL: raw})
			require.ErrorIs(t, err, ticket.ErrInvalidPRURL)

			// Refused before anything was written: the agent still holds the
			// ticket and can call again with a corrected link.
			_, err = f.svc.Done(f.ctx, agentID, &ticket.DoneParams{Summary: "merged"})
			require.NoError(t, err)
		})
	}
}

func TestDone_AcceptsAndReturnsAPRURL(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	agentID := f.agent(t, "builder-1")
	for name, raw := range map[string]string{
		"github":        aPR,
		"port":          "https://git.example.test:8443/org/repo/-/merge_requests/3",
		"query":         "https://example.test/review?id=42#top",
		"max length":    "https://example.test/" + strings.Repeat("a", 2048-len("https://example.test/")),
		"unicode host":  "https://exämple.test/pull/7",
		"encoded space": "https://example.test/a%20b",
	} {
		t.Run(name, func(t *testing.T) {
			f.ticket(t, "fix it "+name)
			f.claim(t, agentID)

			got, err := f.svc.Done(f.ctx, agentID, &ticket.DoneParams{Summary: "merged", PRURL: raw})
			require.NoError(t, err)
			require.NotNil(t, got.PRURL)
			assert.Equal(t, raw, *got.PRURL, "stored exactly as reported")
		})
	}
}

func TestDone_WithoutAPRURL_LeavesItNull(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	agentID := f.agent(t, "builder-1")
	f.ticket(t, "fix it")
	f.claim(t, agentID)

	got, err := f.svc.Done(f.ctx, agentID, &ticket.DoneParams{Summary: "merged"})
	require.NoError(t, err)
	assert.Nil(t, got.PRURL)
}

// Against the database: the link survives the round trip through the
// column and back out of every read, and a bad one closes nothing.
func TestDone_StoresThePRURL(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	f.ticket(t, "Ship it", "server/")
	f.claim(t, a)

	finished, err := f.svc.Done(f.ctx, a, &ticket.DoneParams{Summary: "merged", PRURL: aPR})
	require.NoError(t, err)
	require.NotNil(t, finished.PRURL)
	assert.Equal(t, aPR, *finished.PRURL)

	got := f.get(t, finished.ID)
	require.NotNil(t, got.PRURL)
	assert.Equal(t, aPR, *got.PRURL)

	listed, err := f.svc.List(f.ctx, nil)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	require.NotNil(t, listed[0].PRURL)
	assert.Equal(t, aPR, *listed[0].PRURL)
}

func TestDone_AnInvalidPRURLClosesNothing(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	created := f.ticket(t, "Ship it", "server/")
	f.claim(t, a)

	_, err := f.svc.Done(f.ctx, a, &ticket.DoneParams{Summary: "merged", PRURL: "javascript:alert(1)"})
	require.ErrorIs(t, err, ticket.ErrInvalidPRURL)

	assert.Equal(t, ticket.StatusInProgress, f.status(t, created.ID))
	comments, err := f.svc.ListComments(f.ctx, created.ID)
	require.NoError(t, err)
	assert.Empty(t, comments, "the summary is not recorded for a done that was refused")
	_, err = f.svc.LeaseFor(f.ctx, a)
	require.NoError(t, err, "the agent still holds the ticket")
}

// The schema's CHECK is the backstop for a write that skips the service.
func TestPRURLColumn_RefusesANonHTTPSLink(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	created := f.ticket(t, "Ship it", "server/")

	_, err := f.database.Exec(f.ctx,
		`UPDATE attempts SET pr_url = 'javascript:alert(1)' WHERE ticket_id = $1 AND number = 1`, created.ID)
	require.Error(t, err)
}

// A ticket reopened by a human and finished again without a PR keeps the
// link it had: that change was still merged.
func TestDone_AgainWithoutAPRURL_KeepsTheEarlierOne(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	created := f.ticket(t, "Ship it", "server/")
	f.claim(t, a)
	_, err := f.svc.Done(f.ctx, a, &ticket.DoneParams{Summary: "merged", PRURL: aPR})
	require.NoError(t, err)

	_, err = f.setStatus(t, created.ID, ticket.StatusOpen)
	require.NoError(t, err)
	f.claim(t, a)
	_, err = f.svc.Done(f.ctx, a, &ticket.DoneParams{Summary: "follow-up, nothing to merge"})
	require.NoError(t, err)

	got := f.get(t, created.ID)
	require.NotNil(t, got.PRURL)
	assert.Equal(t, aPR, *got.PRURL)
}
