package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestClosers_CloseInReverseAndOnce(t *testing.T) {
	t.Parallel()
	var ran, told []string
	cl := &closers{closing: func(name string) { told = append(told, name) }}
	for _, name := range []string{"first", "second", "third"} {
		cl.push(name, func() { ran = append(ran, name) })
	}

	cl.close()
	cl.close()

	assert.Equal(t, []string{"third", "second", "first"}, ran)
	assert.Equal(t, ran, told, "each closer is announced as it runs")
}

func TestClosers_APanickingCloserLeavesTheRestToRun(t *testing.T) {
	t.Parallel()
	var ran []string
	cl := &closers{}
	cl.push("logs", func() { ran = append(ran, "logs") })
	cl.push("broken", func() { panic("closing broken") })
	cl.push("pruner", func() { ran = append(ran, "pruner") })

	assert.PanicsWithValue(t, "closing broken", cl.close)
	assert.Equal(t, []string{"pruner", "logs"}, ran)
}
