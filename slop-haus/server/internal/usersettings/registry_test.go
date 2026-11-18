package usersettings_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/usersettings"
)

func TestRenderMarkdownIsAGlobalBoolDefaultingOn(t *testing.T) {
	def, ok := usersettings.DefaultRegistry().Lookup(usersettings.RenderMarkdown)
	require.True(t, ok)
	assert.Equal(t, usersettings.ScopeGlobal, def.Scope)
	assert.Equal(t, usersettings.BoolValue(true), def.Default)
	assert.NoError(t, usersettings.DefaultRegistry().Check(usersettings.RenderMarkdown, usersettings.BoolValue(false)))
	assert.Error(t, usersettings.DefaultRegistry().Check(usersettings.RenderMarkdown, usersettings.StrValue("x")))
}
