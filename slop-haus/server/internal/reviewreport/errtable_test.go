package reviewreport

import (
	"testing"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/httperr"
)

// A sentinel missing from the table would answer 500, which the client
// retries forever.
func TestErrTableCoversEverySentinel(t *testing.T) {
	httperr.RequireCovered(t, &errTable, map[string]error{
		"ErrInvalid":          ErrInvalid,
		"ErrRateLimited":      ErrRateLimited,
		"ErrRunTaken":         ErrRunTaken,
		"ErrFindingTaken":     ErrFindingTaken,
		"ErrNoFinding":        ErrNoFinding,
		"ErrNoProviderConfig": ErrNoProviderConfig,
	}, map[string]string{
		"ErrNoHeldAttempt": "MCP only: review_request and review_findings answer it as text",
		"ErrNotSupervised": "MCP only: review_request answers it as text",
		"ErrNoRequest":     "MCP only: review_findings answers it as text",
	})
}
