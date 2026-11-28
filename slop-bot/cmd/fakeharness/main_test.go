package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/lowaa/slop-factory/slop-bot/internal/harness/fake"
)

func TestRunExitsCleanlyWhenStdinCloses(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	assert.Equal(t, 0, run(nil, strings.NewReader(""), &out, func(string) string { return "" }))
}

func TestRunToleratesNilStdinAndEnv(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	assert.Equal(t, 0, run(nil, nil, &out, nil))
}

func TestRunFailsWhenTheControlSocketIsUnreachable(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	getenv := func(k string) string {
		if k == fake.EnvControl {
			return "/nonexistent/ctl.sock"
		}
		return ""
	}
	assert.Equal(t, 70, run(nil, strings.NewReader(""), &out, getenv), "a harness that can't reach its controller reports failure")
	assert.Contains(t, out.String(), `"ev":"error"`)
}
