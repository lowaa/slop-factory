package github_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/github"
)

const review = `{"action":"submitted","review":{"state":"Changes_Requested","body":"Split it.",
"html_url":"https://github.com/Acme/Widgets/pull/12#pullrequestreview-9","submitted_at":"2026-01-02T03:04:05Z",
"user":{"id":7,"login":"rev"}},"pull_request":{"html_url":"https://github.com/Acme/Widgets/pull/12"}}`

func TestParseEvent_AReview(t *testing.T) {
	ev := github.New().ParseEvent("pull_request_review", []byte(review))
	assert.Equal(t, &codehost.Event{
		Kind:        codehost.EventReview,
		Change:      codehost.ChangeRef{Host: "github.com", Owner: "acme", Repo: "widgets", Number: 12},
		Actor:       codehost.Actor{ID: 7, Login: "rev"},
		Verdict:     codehost.VerdictChangesRequested,
		SubmittedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		Body:        "Split it.",
		URL:         "https://github.com/Acme/Widgets/pull/12#pullrequestreview-9",
	}, ev)
}

func TestParseEvent_AMerge(t *testing.T) {
	ev := github.New().ParseEvent("pull_request", []byte(
		`{"action":"closed","pull_request":{"html_url":"https://github.com/acme/widgets/pull/3","merged":true,"merged_by":{"id":5,"login":"m"},"merge_commit_sha":"abc123","base":{"ref":"slop-haus-base-ticket-4"}}}`))
	require.Equal(t, codehost.EventMerged, ev.Kind)
	assert.Equal(t, "slop-haus-base-ticket-4", ev.Base)
	assert.Equal(t, codehost.ChangeRef{Host: "github.com", Owner: "acme", Repo: "widgets", Number: 3}, ev.Change)
	assert.Equal(t, codehost.Actor{ID: 5, Login: "m"}, ev.Actor)
	assert.Equal(t, "abc123", ev.MergeSHA)
}

func TestParseEvent_IgnoredWithTheReason(t *testing.T) {
	for _, tc := range []struct{ name, event, body, reason string }{
		{"closed unmerged", "pull_request", `{"action":"closed","pull_request":{"html_url":"https://github.com/a/b/pull/1"}}`, "only a merged pull request"},
		{"commented review", "pull_request_review", `{"action":"submitted","review":{"state":"commented"}}`, "a review that is commented changes nothing"},
		{"review of an issue link", "pull_request_review", `{"action":"submitted","review":{"state":"approved","submitted_at":"2026-01-02T03:04:05Z","user":{"id":1}},"pull_request":{"html_url":"https://github.com/a/b/issues/1"}}`, "not a github.com pull request"},
		{"review without a reviewer", "pull_request_review", `{"action":"submitted","review":{"state":"approved","submitted_at":"2026-01-02T03:04:05Z"},"pull_request":{"html_url":"https://github.com/a/b/pull/1"}}`, "names no reviewer"},
		{"review without a time", "pull_request_review", `{"action":"submitted","review":{"state":"approved","user":{"id":1}},"pull_request":{"html_url":"https://github.com/a/b/pull/1"}}`, "names no reviewer"},
		{"merge without a merger", "pull_request", `{"action":"closed","pull_request":{"html_url":"https://github.com/a/b/pull/1","merged":true}}`, "nobody who merged"},
		{"other event", "push", `{}`, `does not act on "push"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ev := github.New().ParseEvent(tc.event, []byte(tc.body))
			assert.Equal(t, codehost.EventIgnored, ev.Kind)
			assert.Contains(t, ev.Ignore, tc.reason)
		})
	}
}

func TestParseRemote(t *testing.T) {
	for remote, want := range map[string]*codehost.Repo{
		"github.com/acme/app":         {Owner: "acme", Name: "app"},
		"GitHub.com/Acme/App":         {Owner: "acme", Name: "app"},
		"github.com/acme":             nil,
		"github.com/acme/app/sub":     nil,
		"github.com//app":             nil,
		"gitlab.com/acme/app":         nil,
		"github.com.evil.test/a/b":    nil,
		"https://github.com/acme/app": nil,
		"":                            nil,
	} {
		t.Run(remote, func(t *testing.T) {
			got, ok := github.New().ParseRemote(remote)
			if want == nil {
				assert.False(t, ok)
				return
			}
			require.True(t, ok)
			assert.Equal(t, *want, got)
		})
	}
}

func TestParseChangeURL(t *testing.T) {
	ref := &codehost.ChangeRef{Host: "github.com", Owner: "acme", Repo: "widgets", Number: 12}
	for raw, want := range map[string]*codehost.ChangeRef{
		"https://github.com/Acme/Widgets/pull/12":          ref,
		"https://GitHub.com/acme/widgets/pull/12/files":    ref,
		"https://github.com/acme/widgets/pull/12#pullreq":  ref,
		"https://github.com/acme/widgets/pull/12?w=1":      ref,
		"https://github.com/acme/widgets/pull/012":         nil,
		"https://github.com/acme/widgets/pull/+12":         nil,
		"https://github.com/acme/widgets/pull/0":           nil,
		"https://github.com/acme/widgets/issues/12":        nil,
		"https://github.com/acme/widgets/pull/":            nil,
		"https://github.com/acme/widgets":                  nil,
		"http://github.com/acme/widgets/pull/12":           nil,
		"https://github.example/acme/widgets/pull/12":      nil,
		"https://github.com.evil.example/acme/w/pull/12":   nil,
		"https://user@github.com/acme/widgets/pull/12":     nil,
		"https://github.com//widgets/pull/12":              nil,
		"https://github.com/acme/widgets/pull/99999999999": {Host: "github.com", Owner: "acme", Repo: "widgets", Number: 99999999999},
	} {
		t.Run(raw, func(t *testing.T) {
			got, ok := github.New().ParseChangeURL(raw)
			if want == nil {
				assert.False(t, ok)
				return
			}
			require.True(t, ok)
			assert.Equal(t, *want, got)
		})
	}
	assert.Equal(t, "https://github.com/acme/widgets/pull/12", github.New().ChangeURL(*ref))
}
