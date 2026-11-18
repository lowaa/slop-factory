package email

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNew_SMTPBackend(t *testing.T) {
	sender := New(&Config{Backend: "smtp", SMTPHost: "smtp.example.com", SMTPPort: "587", From: "noreply@example.com"}, nil)
	assert.IsType(t, &SMTPSender{}, sender)
}

func TestNew_LogBackend(t *testing.T) {
	sender := New(&Config{Backend: "log"}, nil)
	assert.IsType(t, LogSender{}, sender)
}

// New falls back to LogSender for any Backend other than "smtp" -- callers
// that want an unrecognized backend to be a hard error validate it
// themselves before calling New (see Config.Backend's doc comment).
func TestNew_UnknownBackendFallsBackToLog(t *testing.T) {
	sender := New(&Config{Backend: "bogus"}, nil)
	assert.IsType(t, LogSender{}, sender)
}

func TestNew_NilLoggerDefaultsToSlogDefault(t *testing.T) {
	assert.NotPanics(t, func() {
		New(&Config{Backend: "log"}, nil)
	})
}
