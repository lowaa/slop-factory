package pendingaction

import (
	"testing"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/httperr"
)

// A sentinel missing from the table would answer 500.
func TestErrTableCoversEverySentinel(t *testing.T) {
	httperr.RequireCovered(t, &errTable, map[string]error{
		"ErrNotFound":       ErrNotFound,
		"ErrTicketNotFound": ErrTicketNotFound,
		"ErrNotApprover":    ErrNotApprover,
		"ErrNotConsole":     ErrNotConsole,
		"ErrTicketHeld":     ErrTicketHeld,
		"ErrInvalid":        ErrInvalid,
		"ErrTooManyPending": ErrTooManyPending,
		"ErrRateLimited":    ErrRateLimited,
		"ErrNotPending":     ErrNotPending,
		"ErrStale":          ErrStale,
		"ErrVoided":         ErrVoided,
		"ErrRefused":        ErrRefused,
		"ErrBlocked":        ErrBlocked,
		"ErrNotRevertable":  ErrNotRevertable,
		"ErrNotConfigured":  ErrNotConfigured,
	}, map[string]string{
		"ErrTicketNotFound": "refused to the proposing agent (MCP), not by the board handler",
		"ErrNotConsole":     "refused to the proposing agent (MCP), not by the board handler",
		"ErrTicketHeld":     "refused to the proposing agent (MCP), not by the board handler",
		"ErrTooManyPending": "refused to the proposing agent (MCP), not by the board handler",
		"ErrRateLimited":    "refused to the proposing agent (MCP), not by the board handler",
		"ErrNotConfigured":  "a server wired without what deciding needs: a logged 500 is the answer",
	})
}
