package main

import (
	"bytes"
	"log/slog"
	"testing"

	"github.com/lowaa/slop-factory/go-auth/email"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/config"
)

// The log backend writes whole emails, which carry sign-in links, so what it
// writes must reach the dev sink alone, never the operational handler that
// feeds the log store.
func TestLogBackendMailGoesToTheDevSinkOnly(t *testing.T) {
	t.Parallel()
	var operational, devSink bytes.Buffer
	cfg := &config.Config{Email: email.Config{Backend: "log"}}

	mailer := newMailer(cfg, slog.NewTextHandler(&operational, nil), slog.NewTextHandler(&devSink, nil))
	require.NoError(t, mailer.Send(t.Context(), "ann@example.com", "Activate", "https://app/activate?token=abc"))

	assert.Contains(t, devSink.String(), "token=abc")
	assert.NotContains(t, operational.String(), "token=abc")
	assert.NotContains(t, operational.String(), "ann@example.com")
	assert.Contains(t, operational.String(), "EMAIL_BACKEND=log", "the operational log says the sink is in use")
}

func TestSMTPBackendNeverUsesTheDevSink(t *testing.T) {
	t.Parallel()
	var operational, devSink bytes.Buffer
	cfg := &config.Config{Email: email.Config{Backend: "smtp", SMTPHost: "smtp.invalid", From: "a@example.com"}}

	mailer := newMailer(cfg, slog.NewTextHandler(&operational, nil), slog.NewTextHandler(&devSink, nil))

	_, isLog := mailer.(email.LogSender)
	assert.False(t, isLog)
	assert.Empty(t, devSink.String())
	assert.Empty(t, operational.String())
}
