package ident

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPrincipalValidity(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		p    Principal
		want bool
	}{
		{"user", UserPrincipal(), true},
		{"agent", AgentPrincipal("a1", "7"), true},
		{"zero", Principal{}, false},
		{"agent without ticket", Principal{Kind: Agent, Agent: "a1"}, false},
		{"user with agent fields", Principal{Kind: User, Agent: "a1"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, c.want, c.p.Valid())
		})
	}
}

func TestPrincipalString(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "user", UserPrincipal().String())
	assert.Equal(t, "agent a1 (ticket 7)", AgentPrincipal("a1", "7").String())
}
