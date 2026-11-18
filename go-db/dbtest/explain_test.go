package dbtest

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestExplainAppendsDiagnosis(t *testing.T) {
	assert.Equal(t, "boom", explain("boom"), "no hook, no change")

	prev := opts
	t.Cleanup(func() { opts = prev })
	opts.Diagnose = func(context.Context) string { return "container gone" }
	assert.Equal(t, "boom\ncontainer gone", explain("boom"))
}
