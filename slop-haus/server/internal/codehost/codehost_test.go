package codehost_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/codehosttest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/github"
)

// What every Host owes its callers, whichever it is: a delivery it does
// not act on is an ignored Event with a reason, never nil and never a
// panic, since the receiver is open to the internet.
func TestHosts_AnUnintelligibleDeliveryIsIgnoredWithAReason(t *testing.T) {
	hosts := map[string]codehost.Host{
		"github": github.New(),
		"fake":   &codehosttest.Fake{Domain: "git.example.test"},
	}
	for name, h := range hosts {
		for _, body := range []string{"", "not json", "null", "[]", `{"action":`} {
			t.Run(name, func(t *testing.T) {
				for _, event := range []string{"pull_request", "pull_request_review", "nonsense"} {
					ev := h.ParseEvent(event, []byte(body))
					require.NotNil(t, ev)
					assert.Equal(t, codehost.EventIgnored, ev.Kind, "%s %q", event, body)
					assert.NotEmpty(t, ev.Ignore)
				}
			})
		}
	}
}

func TestHosts_AGarbageRemoteIsNotARepository(t *testing.T) {
	for _, remote := range []string{"", "/", "github.com", "github.com/", "../..", "git.example.test"} {
		_, ok := github.New().ParseRemote(remote)
		assert.False(t, ok, "%q", remote)
		_, ok = (&codehosttest.Fake{Domain: "git.example.test"}).ParseRemote(remote)
		assert.False(t, ok, "%q", remote)
	}
}
