package main

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/githubapp"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/githubhook"
)

// What the GitHub App's webhook does that the review and merge tests above
// don't: the route it is configured on, and which installations it answers
// for.

// The URL slop-haus sets on GitHub (githubapp) and the route the receiver
// serves (githubhook) are written down in two packages that cannot import
// each other.
func TestContractWebhookApp_TheConfiguredPathIsTheServedPath(t *testing.T) {
	t.Parallel()
	assert.Equal(t, githubapp.AppWebhookPath, githubhook.AppReceivePath)
}

func TestContractWebhookApp(t *testing.T) {
	t.Parallel()
	t.Run("one review reported twice under different delivery ids is applied once", func(t *testing.T) {
		rig := newWebhookRig(t)
		f := rig.inReview()
		review := reviewPayload{state: "approved", submitted: later()}

		rig.received(&post{event: "pull_request_review", body: reviewEvent(review)}, "applied: #1 approved")
		assert.NotNil(t, f.ticket()["approved_at"])
		rig.received(&post{event: "pull_request_review", body: reviewEvent(review)},
			"ignored: already applied from another delivery")
	})

	t.Run("an unknown installation is ignored, not refused", func(t *testing.T) {
		rig := newWebhookRig(t)
		f := rig.inReview()

		hook := rig.webhook()
		got := rig.send(&post{event: "pull_request_review", body: []byte(`{"installation":{"id":1}}`), secret: hook.secret})
		assert.Equal(t, http.StatusOK, got.code)
		assert.Equal(t, "ignored: no GitHub App installation here\n", got.text)
		assert.Equal(t, "review", f.ticket()["status"])
	})
}
