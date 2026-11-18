package main

import (
	"testing"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/landing"
	"github.com/stretchr/testify/assert"
)

// A nil *Finisher inside the interface would read as a lander that is there,
// and the kill switch would not leave tickets their lock tools.
func TestLandingModuleKillSwitchLeavesMCPSeamsNil(t *testing.T) {
	off := &landingModule{}
	assert.True(t, off.Lander() == nil)
	assert.True(t, off.QueueMode() == nil)
}

func TestLandingModuleExposesFinisherAndModeToMCP(t *testing.T) {
	f := &landing.Finisher{}
	mode := landing.NewSettingsMode(nil, true)
	on := &landingModule{Finisher: f, mode: mode}
	assert.Same(t, f, on.Lander())
	assert.Equal(t, mode, on.QueueMode())
}
