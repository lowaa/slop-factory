package agent

import (
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/danielgtaylor/huma/v2"
	"github.com/stretchr/testify/require"
	"slices"
	"testing"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/httperr"
)

// A sentinel missing from the table would answer 500.
func TestErrTableCoversEverySentinel(t *testing.T) {
	allRules := httperr.Table{Rules: slices.Concat(supervisorRules, provisionRules, hostLeaseRules, mainRules), Custom: claimedByUser}
	httperr.RequireCovered(t, &allRules, map[string]error{
		"ErrArchived":               ErrArchived,
		"ErrBadKind":                ErrBadKind,
		"ErrBadToken":               ErrBadToken,
		"ErrAgentKindInvalid":       ErrAgentKindInvalid,
		"ErrAgentNameTaken":         ErrAgentNameTaken,
		"ErrAgentUnavailable":       ErrAgentUnavailable,
		"ErrGrantNoApprover":        ErrGrantNoApprover,
		"ErrGrantNotFound":          ErrGrantNotFound,
		"ErrReplaceUnavailable":     ErrReplaceUnavailable,
		"ErrSlotCapInvalid":         ErrSlotCapInvalid,
		"ErrTooManySlotCredentials": ErrTooManySlotCredentials,
		"ErrDesiredInvalid":         ErrDesiredInvalid,
		"ErrDesiredNameHeld":        ErrDesiredNameHeld,
		"ErrImportUnavailable":      ErrImportUnavailable,
		"ErrEmptyName":              ErrEmptyName,
		"ErrIsDisabled":             ErrIsDisabled,
		"ErrNameRetired":            ErrNameRetired,
		"ErrNameTaken":              ErrNameTaken,
		"ErrNotAdoptable":           ErrNotAdoptable,
		"ErrNotDisabled":            ErrNotDisabled,
		"ErrNotFound":               ErrNotFound,
		"ErrNotOwner":               ErrNotOwner,
		"ErrNotReplaceable":         ErrNotReplaceable,
		"ErrNotSupervisor":          ErrNotSupervisor,
		"ErrNotSupervisorCaller":    ErrNotSupervisorCaller,
		"ErrNotTenantOwner":         ErrNotTenantOwner,
		"ErrRoleInUse":              ErrRoleInUse,
		"ErrRoleInvalid":            ErrRoleInvalid,
		"ErrRoleNameTaken":          ErrRoleNameTaken,
		"ErrRoleNotFound":           ErrRoleNotFound,
		"ErrSlotCapReached":         ErrSlotCapReached,
		"ErrSlotCredentialsOff":     ErrSlotCredentialsOff,
		"ErrStaleRevision":          ErrStaleRevision,
		"ErrHostLeaseHeld":          ErrHostLeaseHeld,
		"ErrHostLeaseLost":          ErrHostLeaseLost,
		"ErrNotRemoteControl":       ErrNotRemoteControl,
		"ErrHolderInvalid":          ErrHolderInvalid,
		"ErrRunBySupervisor":        ErrRunBySupervisor,
		"ErrNotApprover":            ErrNotApprover,
		"ErrProvisionOff":           ErrProvisionOff,
		"ErrSupervisorOff":          ErrSupervisorOff,
	}, map[string]string{
		"ErrRoleInUse":          "answered by DeleteRole's own handler as a *RoleInUseError body, which carries the tickets",
		"ErrNotReplaceable":     "the OAuth registration path turns it into ErrReplaceUnavailable",
		"ErrBadKind":            "the OAuth registration path turns it into ErrAgentKindInvalid",
		"ErrBadToken":           "raised by token authentication, which answers 401 itself",
		"ErrAgentKindInvalid":   "answered by the OAuth handler's own table",
		"ErrAgentNameTaken":     "answered by the OAuth handler's own table",
		"ErrAgentUnavailable":   "answered by the OAuth handler's own table",
		"ErrReplaceUnavailable": "answered by the OAuth handler's own table",
	})
}

func TestErrTableAnswersTheDocumentedStatus(t *testing.T) {
	h := &Handler{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	cases := []struct {
		name   string
		mapFn  func(error) error
		err    error
		status int
		detail string
	}{
		{"owner", h.mapError, ErrNotOwner, 403, "only the conductor's owner may do that"},
		{"tenant owner", h.mapError, ErrNotTenantOwner, 403, "only the team's owner may do that"},
		{"not found", h.mapError, ErrNotFound, 404, "context: " + ErrNotFound.Error()},
		{"role not found", h.mapError, ErrRoleNotFound, 404, "context: " + ErrRoleNotFound.Error()},
		{"empty name", h.mapError, ErrEmptyName, 422, "context: " + ErrEmptyName.Error()},
		{"name taken", h.mapError, ErrNameTaken, 422, "context: " + ErrNameTaken.Error()},
		{"role invalid", h.mapError, ErrRoleInvalid, 422, "context: " + ErrRoleInvalid.Error()},
		{"role name taken", h.mapError, ErrRoleNameTaken, 422, "context: " + ErrRoleNameTaken.Error()},
		{"not disabled", h.mapError, ErrNotDisabled, 409, "agent is enabled: disable it first"},
		{"archived", h.mapError, ErrArchived, 409, "agent is archived: unarchive it first"},
		{"disabled", h.mapError, ErrIsDisabled, 409, "agent is disabled: enable it first"},
		{"stale revision", h.mapProvisionError, ErrStaleRevision, 409, "the desired state changed since you read it: read it again and retry"},
		{"name held", h.mapProvisionError, ErrDesiredNameHeld, 409, "context: " + ErrDesiredNameHeld.Error()},
		{"desired invalid", h.mapProvisionError, ErrDesiredInvalid, 422, "context: " + ErrDesiredInvalid.Error()},
		{"not approver", h.mapProvisionError, ErrNotApprover, 403, "only the person who approved the supervisor may do that"},
		{"provision off", h.mapProvisionError, ErrProvisionOff, 409, "the team does not provision agents from the board: its owner can turn that on"},
		{"supervisor off", h.mapProvisionError, ErrSupervisorOff, 409, "that supervisor is switched off"},
		{"slot cap invalid", h.mapProvisionError, ErrSlotCapInvalid, 422, "slot_cap must be between 1 and the maximum"},
		{"run by supervisor", h.mapError, ErrRunBySupervisor, 409, "a supervisor runs this remote control: remove it from the supervisor instead"},
		{"slot disabled", h.mapSupervisorError, ErrIsDisabled, 409, "that slot or steward is disabled"},
		{"supervisor archived", h.mapSupervisorError, ErrArchived, 403, "the supervisor cannot do this"},
		{"slot cap", h.mapSupervisorError, ErrSlotCapReached, 409, "the supervisor is at its slot cap: retire a slot first"},
		{"supervisor name taken", h.mapSupervisorError, ErrNameTaken, 409, "an agent with that name already exists: for the steward, an archived one holds it, so unarchive that one"},
		{"too many credentials", h.mapSupervisorError, ErrTooManySlotCredentials, 429, "too many credentials requested"},
		{"credentials off", h.mapSupervisorError, ErrSlotCredentialsOff, 503, "slot credentials are not available"},
		{"fall through to main", h.mapSupervisorError, ErrNotOwner, 403, "only the conductor's owner may do that"},
		{"unexpected", h.mapError, errors.New("secret internals"), 500, "internal server error"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var se huma.StatusError
			require.ErrorAs(t, c.mapFn(fmt.Errorf("context: %w", c.err)), &se)
			require.Equal(t, c.status, se.GetStatus())
			require.Equal(t, c.detail, se.Error())
		})
	}
}
