package codehost_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/codehosttest"
)

func twoHosts() (*codehost.Registry, *codehosttest.Fake, *codehosttest.Fake) {
	a := &codehosttest.Fake{
		Domain:  "a.example",
		Changes: map[string]codehost.ChangeRef{"https://a.example/x/y/pull/1": {Host: "a.example", Owner: "x", Repo: "y", Number: 1}},
		Remotes: map[string]codehost.Repo{"a.example/x/y": {Owner: "x", Name: "y"}},
	}
	b := &codehosttest.Fake{
		Domain:  "b.example",
		Changes: map[string]codehost.ChangeRef{"https://b.example/g/s/p/-/merge_requests/2": {Host: "b.example", Owner: "g/s", Repo: "p", Number: 2}},
		Remotes: map[string]codehost.Repo{"b.example/g/s/p": {Owner: "g/s", Name: "p"}},
	}
	reg := codehost.NewRegistry()
	reg.Register(a, a)
	reg.Register(b, nil)
	return reg, a, b
}

func TestRegistry_PicksTheHostByLinkAndRemote(t *testing.T) {
	reg, _, _ := twoHosts()

	ref, ok := reg.ParseChangeURL("https://b.example/g/s/p/-/merge_requests/2")
	require.True(t, ok)
	assert.Equal(t, codehost.ChangeRef{Host: "b.example", Owner: "g/s", Repo: "p", Number: 2}, ref)
	assert.Equal(t, codehost.Repo{Owner: "g/s", Name: "p"}, ref.InRepo(), "a nested group fits in Owner")

	host, repo, ok := reg.ParseRemote("b.example/g/s/p")
	require.True(t, ok)
	assert.Equal(t, "b.example", host)
	assert.Equal(t, codehost.Repo{Owner: "g/s", Name: "p"}, repo)
}

func TestRegistry_AnUnknownLinkOrRemoteIsNotAnyHosts(t *testing.T) {
	reg, _, _ := twoHosts()

	_, ok := reg.ParseChangeURL("https://c.example/x/y/pull/1")
	assert.False(t, ok)
	_, _, ok = reg.ParseRemote("c.example/x/y")
	assert.False(t, ok, "no registered host")
	_, _, ok = reg.ParseRemote("a.example/not/a/remote")
	assert.False(t, ok, "the host does not recognise it")
	assert.Equal(t, "https://a.example/x/y/pull/1", reg.ChangeURL(codehost.ChangeRef{Host: "a.example", Owner: "x", Repo: "y", Number: 1}))
	assert.Empty(t, reg.ChangeURL(codehost.ChangeRef{Host: "c.example", Owner: "x", Repo: "y", Number: 1}))
}

func TestRegistry_ChangesOnlyForAHostRegisteredWithThem(t *testing.T) {
	reg, a, _ := twoHosts()

	got, ok := reg.Changes("A.example")
	require.True(t, ok)
	assert.Same(t, a, got)
	_, ok = reg.Changes("b.example")
	assert.False(t, ok, "registered for reading only")
	_, ok = reg.Changes("c.example")
	assert.False(t, ok)
}

func TestRegistry_ARepeatedNameIsAWiringMistake(t *testing.T) {
	reg, a, _ := twoHosts()
	assert.Panics(t, func() { reg.Register(a, nil) })
}

func TestRegistry_RegisterLateReadsTheOperationsWhenAsked(t *testing.T) {
	_, _, b := twoHosts()
	reg := codehost.NewRegistry()
	var bound codehost.Changes
	reg.RegisterLate(b, func() codehost.Changes { return bound })

	bound = b
	got, ok := reg.Changes("b.example")
	require.True(t, ok)
	assert.Same(t, b, got)
	assert.Panics(t, func() { reg.RegisterLate(b, func() codehost.Changes { return b }) }, "a repeated name is a wiring mistake")
}
