package tuidriver

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestARenderedPasteIsANewOccurrence(t *testing.T) {
	t.Parallel()
	p := &Profile{}
	const ptr = "1 new message: run `slop-bot inbox`"
	assert.True(t, p.rendered("> ", "> 1 new message: run\n`slop-bot inbox`", ptr), "wrapped across lines")
	assert.False(t, p.rendered("> "+ptr+"\n> ", "> "+ptr+"\n> ", ptr), "an earlier delivery still on screen is not this paste")
	assert.True(t, p.rendered("> "+ptr+"\n> ", "> "+ptr+"\n> "+ptr, ptr))
}
