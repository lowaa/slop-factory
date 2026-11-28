package main

import (
	"bufio"
	"context"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/scriptedmodel"
)

func writeScenario(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "scenario.json")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

var servingLine = regexp.MustCompile(`msg=serving url=(\S+)`)

func TestRunServesTheScenarioFileUntilCancelled(t *testing.T) {
	t.Parallel()
	path := writeScenario(t, `{"name": "s", "turns": [{"text": "a"}, {"text": "b"}]}`)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	logs, logWriter := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"-listen", "127.0.0.1:0", "-scenario", path}, logWriter)
		_ = logWriter.Close()
	}()

	// The log line that names the URL is the point at which it serves.
	lines := bufio.NewScanner(logs)
	url := ""
	for url == "" && lines.Scan() {
		if m := servingLine.FindStringSubmatch(lines.Text()); m != nil {
			url = m[1]
		}
	}
	require.NotEmpty(t, url, "no serving line")
	go func() { _, _ = io.Copy(io.Discard, logs) }()

	remaining, err := scriptedmodel.NewClient(url, nil).Remaining(t.Context())
	require.NoError(t, err)
	assert.Equal(t, 2, remaining, "the scenario file was loaded")

	cancel()
	require.NoError(t, <-done)
}

func TestRunRefuses(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"an unknown flag", []string{"-nope"}, "flag provided but not defined"},
		{"a mistyped subcommand", []string{"ctrl", "load", "f.json"}, `unexpected argument "ctrl"`},
		{"a missing scenario file", []string{"-scenario", filepath.Join(t.TempDir(), "missing.json")}, "no such file"},
		{"an invalid scenario", []string{"-scenario", writeScenario(t, `{"turns": [{}]}`)}, "needs text"},
		{"an address it cannot listen on", []string{"-listen", "256.0.0.1:0"}, "listen on"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := run(t.Context(), tc.args, io.Discard)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}
