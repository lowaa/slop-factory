package conductor

import (
	"testing"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/httperr"
)

// A sentinel missing from the table would answer 500.
func TestErrTableCoversEverySentinel(t *testing.T) {
	httperr.RequireCovered(t, &errTable, map[string]error{
		"ErrNotFound":           ErrNotFound,
		"ErrNotConductor":       ErrNotConductor,
		"ErrBusy":               ErrBusy,
		"ErrQueueFull":          ErrQueueFull,
		"ErrTranscriptFull":     ErrTranscriptFull,
		"ErrHostBusy":           ErrHostBusy,
		"ErrGap":                ErrGap,
		"ErrWorkPending":        ErrWorkPending,
		"ErrNotReplying":        ErrNotReplying,
		"ErrHostLacksInterrupt": ErrHostLacksInterrupt,
		"ErrTurnRunning":        ErrTurnRunning,
		"ErrNoProcess":          ErrNoProcess,
		"ErrCannotCompact":      ErrCannotCompact,
		"ErrInvalid":            ErrInvalid,
	}, map[string]string{})
}
