package cli

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/lowaa/slop-factory/slop-bot/internal/control"
)

// Only the daemon saying no ends an attached TUI: a daemon failing a
// request, or not answering, is retried.
func TestOnlyARefusalEndsAReconnect(t *testing.T) {
	t.Parallel()
	assert.True(t, refusal(fmt.Errorf("reattaching: %w", &control.APIError{Status: http.StatusConflict, Message: "handed back"})))
	assert.False(t, refusal(&control.APIError{Status: http.StatusInternalServerError, Message: "database is locked"}))
	assert.False(t, refusal(errors.New("dial unix: connection refused")))
}
