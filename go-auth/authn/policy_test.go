package authn_test

import (
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/stretchr/testify/assert"

	"github.com/lowaa/slop-factory/go-auth/authn"
)

func opWith(metadata ...map[string]any) *huma.Operation {
	op := &huma.Operation{Metadata: map[string]any{}}
	for _, m := range metadata {
		for k, v := range m {
			op.Metadata[k] = v
		}
	}
	return op
}

func TestAPolicyCountsItsOwnMarkersAndGoAuths(t *testing.T) {
	agent := authn.NewMarker("test:requires-agent", "agent")
	p := authn.NewPolicy(agent)

	assert.Equal(t, 0, p.Markers(opWith()))
	assert.Equal(t, 1, p.Markers(opWith(authn.Public())))
	assert.Equal(t, 1, p.Markers(opWith(authn.RequireUser())))
	assert.Equal(t, 1, p.Markers(opWith(agent.Metadata())))
	assert.Equal(t, 2, p.Markers(opWith(agent.Metadata(), authn.RequireUser())))
	// A marker the policy was not given is no decision at all.
	other := authn.NewMarker("test:other", "other")
	assert.Equal(t, 0, p.Markers(opWith(other.Metadata())))
	assert.Equal(t, 0, p.Markers(nil))
}

func TestMarkerNameNamesTheOneMarker(t *testing.T) {
	agent := authn.NewMarker("test:requires-agent", "agent")
	p := authn.NewPolicy(agent)

	assert.Equal(t, "public", p.MarkerName(opWith(authn.Public())))
	assert.Equal(t, "user", p.MarkerName(opWith(authn.RequireUser())))
	assert.Equal(t, "agent", p.MarkerName(opWith(agent.Metadata())))
	assert.Equal(t, "", p.MarkerName(opWith()))
	assert.Equal(t, "multiple", p.MarkerName(opWith(authn.Public(), agent.Metadata())))
}

func TestAPolicyRefusesTwoMarkersWithOneKey(t *testing.T) {
	assert.Panics(t, func() {
		authn.NewPolicy(authn.NewMarker("test:x", "x"), authn.NewMarker("test:x", "y"))
	})
	assert.Panics(t, func() { authn.NewPolicy(authn.NewMarker("go-auth:public", "mine")) }, "nor may an app's reuse go-auth's")
}
