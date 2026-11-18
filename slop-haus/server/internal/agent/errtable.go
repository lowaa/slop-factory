package agent

import (
	"slices"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/httperr"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/user"
)

// mainRules answer a Service error with the status it carries. The default
// never echoes err: an unrecognised error's text is unvetted.
var mainRules = []httperr.Rule{
	{Err: authn.ErrNotPerson, Kind: httperr.Forbidden, Message: "only a signed-in person can do that"},
	{Err: ErrNotOwner, Kind: httperr.Forbidden, Message: "only the conductor's owner may do that"},
	{Err: ErrNotTenantOwner, Kind: httperr.Forbidden, Message: "only the team's owner may do that"},
	{Err: ErrNotFound, Kind: httperr.NotFound},
	{Err: ErrRoleNotFound, Kind: httperr.NotFound},
	{Err: ErrEmptyName, Kind: httperr.Invalid},
	{Err: ErrNameTaken, Kind: httperr.Invalid},
	{Err: ErrRoleInvalid, Kind: httperr.Invalid},
	{Err: ErrRoleNameTaken, Kind: httperr.Invalid},
	{Err: ErrNotDisabled, Kind: httperr.Conflict, Message: "agent is enabled: disable it first"},
	{Err: ErrArchived, Kind: httperr.Conflict, Message: "agent is archived: unarchive it first"},
	{Err: ErrIsDisabled, Kind: httperr.Conflict, Message: "agent is disabled: enable it first"},
	{Err: ErrRunBySupervisor, Kind: httperr.Conflict, Message: "a supervisor runs this remote control: remove it from the supervisor instead"},
}

// provisionRules are the desired-state document's own failures; they come
// before mainRules in provisionTable.
var provisionRules = []httperr.Rule{
	{Err: ErrStaleRevision, Kind: httperr.Conflict, Message: "the desired state changed since you read it: read it again and retry"},
	{Err: ErrDesiredNameHeld, Kind: httperr.Conflict},
	{Err: ErrDesiredInvalid, Kind: httperr.Invalid},
	{Err: ErrImportUnavailable, Kind: httperr.Unavailable, Message: "importing slots is not available"},
	{Err: ErrNotApprover, Kind: httperr.Forbidden, Message: "only the person who approved the supervisor may do that"},
	{Err: ErrSlotCapInvalid, Kind: httperr.Invalid, Message: "slot_cap must be between 1 and the maximum"},
	{Err: ErrProvisionOff, Kind: httperr.Conflict, Message: "the team does not provision agents from the board: its owner can turn that on"},
	{Err: ErrSupervisorOff, Kind: httperr.Conflict, Message: "that supervisor is switched off"},
}

// supervisorRules are what a supervisor's calls about its slots add. They come
// before mainRules, so a name taken is a conflict here and not the 422 an
// owner's rename gets.
var supervisorRules = []httperr.Rule{
	// The caller's own state was checked by supervisorCaller, so this is the
	// slot's.
	{Err: ErrIsDisabled, Kind: httperr.Conflict, Message: "that slot or steward is disabled"},
	// The supervisor lost its standing between the check and the action: its
	// last grant ended, or its approver's account was deleted.
	{Err: ErrNotSupervisorCaller, Kind: httperr.Forbidden, Message: "only an enabled supervisor's own credential can do this"},
	{Err: ErrNotSupervisor, Kind: httperr.Forbidden, Message: "the supervisor cannot do this"},
	{Err: ErrArchived, Kind: httperr.Forbidden, Message: "the supervisor cannot do this"},
	{Err: ErrGrantNotFound, Kind: httperr.Forbidden, Message: "the supervisor cannot do this"},
	{Err: ErrGrantNoApprover, Kind: httperr.Forbidden, Message: "the supervisor cannot do this"},
	{Err: ErrSlotCapReached, Kind: httperr.Conflict, Message: "the supervisor is at its slot cap: retire a slot first"},
	{Err: ErrNotAdoptable, Kind: httperr.Conflict, Message: "only a worker can be adopted as a slot"},
	{Err: ErrNameRetired, Kind: httperr.Conflict, Message: "an archived slot of this supervisor holds that name and has history, so it cannot be reused: pick another name"},
	{Err: ErrNameTaken, Kind: httperr.Conflict, Message: "an agent with that name already exists: for the steward, an archived one holds it, so unarchive that one"},
	{Err: ErrTooManySlotCredentials, Kind: httperr.TooMany, Message: "too many credentials requested"},
	{Err: ErrSlotCredentialsOff, Kind: httperr.Unavailable, Message: "slot credentials are not available"},
}

// claimedByUser lets the user package's refusals answer as themselves, with
// their codes.
func claimedByUser(err error) error {
	if p := user.ProblemFor(err); p != nil {
		return p
	}
	return nil
}

const unexpectedError = "unexpected agent error"

var errTable = httperr.Table{Rules: mainRules, Custom: claimedByUser, Unexpected: unexpectedError}

var provisionTable = httperr.Table{
	Rules:      slices.Concat(provisionRules, mainRules),
	Custom:     claimedByUser,
	Unexpected: unexpectedError,
}

var supervisorTable = httperr.Table{
	Rules:      slices.Concat(supervisorRules, mainRules),
	Custom:     claimedByUser,
	Unexpected: unexpectedError,
}

var hostLeaseTable = httperr.Table{
	Rules:      slices.Concat(hostLeaseRules, mainRules),
	Custom:     claimedByUser,
	Unexpected: unexpectedError,
}

func (h *Handler) mapError(err error) error {
	return httperr.Map(h.logger, err, &errTable)
}

func (h *Handler) mapProvisionError(err error) error {
	return httperr.Map(h.logger, err, &provisionTable)
}

func (h *Handler) mapSupervisorError(err error) error {
	return httperr.Map(h.logger, err, &supervisorTable)
}
