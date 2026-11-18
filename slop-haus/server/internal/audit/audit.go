// Package audit is slop-haus's set of security events, recorded by
// go-auth/audit. slop-haus has no audit table: each event is a log line --
// message "audit", with a stable action field that log queries key on --
// and a count in slophaus_audit_events_total (see metrics), which alerts
// key on.
package audit

import (
	"log/slog"

	goaudit "github.com/lowaa/slop-factory/go-auth/audit"
)

// Action names an event. Its values are the log's action field and the
// counter's action label, so they are part of what dashboards and alerts
// are written against: add new ones freely, but rename none.
type Action = goaudit.Action

// Recorder records events; New builds one with slop-haus's action set.
type Recorder = goaudit.Recorder

// Counter counts events; *metrics.Metrics is one.
type Counter = goaudit.Counter

const (
	SignIn         Action = "sign_in"
	SignInRefused  Action = "sign_in_refused"
	Reauth         Action = "reauth"
	ReauthRefused  Action = "reauth_refused"
	InviteAccepted Action = "invite_accepted"
	InviteRefused  Action = "invite_refused"
	// TenantRenamed carries the owner, the tenant and the old and new
	// names; TenantRenameRefused is a non-owner trying.
	TenantRenamed       Action = "tenant_renamed"
	TenantRenameRefused Action = "tenant_rename_refused"
	// TenantCreated carries the owner, the new tenant and its name;
	// TenantCreateRefused a self-service creation turned away (at the cap,
	// switched off, or rate limited), with the reason.
	TenantCreated       Action = "tenant_created"
	TenantCreateRefused Action = "tenant_create_refused"
	// TenantRecoloured carries the owner, the tenant and the old and new
	// colour keys (empty for the default); TenantRecolourRefused is a
	// non-owner trying.
	TenantRecoloured      Action = "tenant_recoloured"
	TenantRecolourRefused Action = "tenant_recolour_refused"
	// TenantArchived and TenantUnarchived carry the owner and the tenant;
	// TenantArchiveRefused is an archive or unarchive turned away (not the
	// owner, the last active tenant, over the cap), with the operation and
	// the reason.
	TenantArchived       Action = "tenant_archived"
	TenantUnarchived     Action = "tenant_unarchived"
	TenantArchiveRefused Action = "tenant_archive_refused"
	IdentityLinked       Action = "identity_linked"
	LinkRefused          Action = "link_refused"
	PasswordAdded        Action = "password_added"
	PasswordChanged      Action = "password_changed"
	SignInMethodRemoved  Action = "sign_in_method_removed"

	// TokenReuse is an OAuth code or refresh token presented after it was
	// spent: a copy is in someone else's hands, and the grant it belonged
	// to has been revoked.
	TokenReuse Action = "oauth_token_reuse"
	// ClientRegistered and ClientRegistrationRefused are dynamic client
	// registrations; a refusal is the unclaimed-client cap, not a bad
	// request.
	ClientRegistered          Action = "oauth_client_registered"
	ClientRegistrationRefused Action = "oauth_client_registration_refused"
	AuthorizationApproved     Action = "oauth_authorization_approved"
	AuthorizationDenied       Action = "oauth_authorization_denied"
	DeviceStarted             Action = "device_authorization_started"
	DeviceApproved            Action = "device_authorization_approved"
	DeviceDenied              Action = "device_authorization_denied"
	GrantRevoked              Action = "oauth_grant_revoked"
	// ReadOnlyClientCreated and ReadOnlyClientRevoked are an admin making
	// or ending a read-only client (cmd/smoke_client). ClientSecretRefused
	// is a confidential client presenting the wrong secret: someone
	// guessing, or a secret rotated on one side only.
	ReadOnlyClientCreated Action = "oauth_read_only_client_created"
	ReadOnlyClientRevoked Action = "oauth_read_only_client_revoked"
	ClientSecretRefused   Action = "oauth_client_secret_refused"
	// ReadOnlyRefused is a read-only token reaching anything but a read:
	// the smoke test never does, so a leaked secret being tried.
	ReadOnlyRefused Action = "read_only_refused"
	// SupervisorRouteRefused is a supervisor's credential on a route that is
	// not the supervisor's, or another kind of agent on one that is: a
	// credential used for something it was not issued for.
	SupervisorRouteRefused Action = "supervisor_route_refused"

	// ConsoleToolRefused is an agent that is not a console calling a tool
	// only a console may: someone with a worker's credential trying to put
	// proposals in the person's queue.
	ConsoleToolRefused Action = "console_tool_refused"
	// SecretWriteRefused is an agent's write turned away because its text
	// looked like a credential: the agent, the ticket (when it has one), the
	// tool and the kind of credential -- never the text.
	SecretWriteRefused Action = "secret_write_refused"
	// ProposalCreated, ProposalApproved and ProposalRejected are a console's
	// proposal and a person's decision on it. ProposalRefused is an approval
	// that applied nothing -- the ticket had moved on, the proposer was no
	// longer a console, or the ticket's rules said no -- and the proposal was
	// settled as such.
	ProposalCreated  Action = "proposal_created"
	ProposalApproved Action = "proposal_approved"
	ProposalRejected Action = "proposal_rejected"
	ProposalRefused  Action = "proposal_refused"
	// StewardVetoed and StewardUndone are the owner taking back a decision the
	// steward made in live mode: cancelling one inside its veto window, or
	// reverting one already applied. They name the owner, the proposal and the
	// steward's category.
	StewardVetoed Action = "steward_vetoed"
	StewardUndone Action = "steward_undone"
	// ManifestChanged is the workspace manifest replaced by a console's
	// proposal that an owner approved. It names the owner and the proposing
	// agent. The board's own saves record the saver in the row.
	ManifestChanged Action = "manifest_changed"
	// BaseBranchChanged is an owner saving a different base branch for a
	// repo on the board: it names the owner, the tenant, the repo's path and
	// remote, and the old and new branch (empty for the default branch).
	BaseBranchChanged Action = "base_branch_changed"
	// RepoMoved is an owner moving a manifest repo to another path, with the
	// scopes of the open tickets and intake sources under it: it names the
	// owner, the tenant, the old and new path and how many tickets and
	// intake sources were rewritten.
	RepoMoved Action = "repo_moved"
	// RepoSynced is an owner merging a repo's default branch into its base
	// branch: it names the owner, the tenant, the repo's path, both branches,
	// the outcome (merged, up_to_date or conflict) and the resulting head, or
	// for a conflict the ticket filed to resolve it.
	RepoSynced Action = "repo_synced"

	// OwnerBoundBlockerChanged is an owner-bound session (a conductor, or a
	// console holding a live grant) adding or removing a blocker directly,
	// on any ticket; OwnerBoundBlockerRefused is one whose owner could not be
	// established. Both carry agent_id and, when known, on_behalf_of_user_id.
	OwnerBoundBlockerChanged Action = "owner_bound_blocker_changed"
	OwnerBoundBlockerRefused Action = "owner_bound_blocker_refused"

	// IntakeSourceCreated, IntakeSourceRotated and IntakeSourceRevoked are a
	// person managing an intake source's credential, which lets whoever
	// holds it put work in the tenant's queue. IntakeDowngraded is a
	// delivery that asked for an open ticket and got a preparing one
	// because its source is untrusted; IntakeRateLimited is a source
	// turned away for sending too much.
	IntakeSourceCreated Action = "intake_source_created"
	IntakeSourceRotated Action = "intake_source_rotated"
	IntakeSourceRevoked Action = "intake_source_revoked"
	IntakeDowngraded    Action = "intake_downgraded"
	IntakeRateLimited   Action = "intake_rate_limited"
	// IntakeResolveClosesEnabled is an owner registering a source whose
	// resolve cancels unclaimed tickets; IntakeResolveClosesRefused is
	// someone else asking for it.
	IntakeResolveClosesEnabled Action = "intake_resolve_closes_enabled"
	IntakeResolveClosesRefused Action = "intake_resolve_closes_refused"

	// CodeHostCredentialConnected and CodeHostCredentialRemoved are an owner
	// connecting (or replacing) or removing the credential slop-haus acts
	// on the tenant's code host with.
	CodeHostCredentialConnected Action = "codehost_credential_connected"
	CodeHostCredentialRemoved   Action = "codehost_credential_removed"
	// CodeHostWebhookRotated is an owner replacing the secret the App's
	// webhook deliveries are signed with.
	CodeHostWebhookRotated Action = "codehost_webhook_rotated"

	// ChangeOpened, ChangeMarkedReady, ChangeChecksRead and ChangeMerged are
	// slop-haus acting on the tenant's code host for an agent: the agent, the
	// ticket and the change are in each record. ChangeMergeRefused is the
	// merge gate saying no (its reason is in the record), and
	// ChangeOperationFailed is the host failing or refusing an operation the
	// gate allowed -- including a merge refused because the head moved.
	ChangeOpened      Action = "change_opened"
	ChangeMarkedReady Action = "change_marked_ready"
	ChangeChecksRead  Action = "change_checks_read"
	ChangeMerged      Action = "change_merged"
	// ChangeReviewRequested is a change handed over for review, with the
	// head commit the approval will cover.
	ChangeReviewRequested Action = "change_review_requested"
	ChangeMergeRefused    Action = "change_merge_refused"
	ChangeOperationFailed Action = "change_operation_failed"
	// ChangeClosedByRule is an experiment's change closed because its ticket
	// is done and its attempt has finished; BaseBranchDeleted is the ticket's
	// base branch removed once no open change targeted it.
	ChangeClosedByRule Action = "change_closed_by_rule"
	// ChangeMergedIntoBase is an unpromoted attempt's change merged, on the
	// host, into the ticket's own base branch: nothing reached main, and the
	// attempt was sent to discussion.
	ChangeMergedIntoBase Action = "change_merged_into_base"
	BaseBranchDeleted    Action = "base_branch_deleted"
	// TicketClosedAsMerged is a ticket an owner closed as done because its
	// change was merged on the code host by someone slop-haus did not act on.
	TicketClosedAsMerged Action = "ticket_closed_as_merged"
	// TicketClosedNoChange is a ticket an agent closed as done with no change
	// to merge; the reason it gave is in the record.
	TicketClosedNoChange Action = "ticket_closed_no_change"
	// LandingRequeueRefused is ticket_finish turned away because the head
	// had already gone red in the landing queue and neither changed nor cited
	// a valid flake ticket; it carries the ticket, head and the reason.
	LandingRequeueRefused Action = "landing_requeue_refused"
	// BatchTreeMismatch is a merge-queue batch stopped because a member's
	// merge on the code host did not have the tree the batch tested: untested
	// code reached the base. It carries the ticket, the batch and what differed.
	BatchTreeMismatch Action = "batch_tree_mismatch"
	// AttemptBranchCreated is slop-haus making a ref on the tenant's code
	// host at an attempt's claim: the branch the agent works on (and, for an
	// unpromoted attempt, the ticket's base branch). A failure to make it is
	// ChangeOperationFailed.
	AttemptBranchCreated Action = "attempt_branch_created"

	// AdminRefused is a signed-in user who is not an admin reaching an
	// admin operation. The board never offers them one, so it is someone
	// trying.
	AdminRefused Action = "admin_refused"
	// HarnessConfigChanged is a person changing a harness setting -- the
	// tenant's default or one agent's override. It decides what runs in
	// every agent's process, so each change is on record with who made it.
	HarnessConfigChanged Action = "harness_config_changed"
	// FleetInstructionsVersionSaved and FleetInstructionsDefaultChanged are
	// an owner changing the text every agent's briefing carries: saving a
	// version, and choosing which one the fleet is given (or none).
	// FleetInstructionsRefused is anyone else trying: the board never offers
	// them the form, so it is someone calling the route.
	FleetInstructionsVersionSaved   Action = "fleet_instructions_version_saved"
	FleetInstructionsDefaultChanged Action = "fleet_instructions_default_changed"
	FleetInstructionsRefused        Action = "fleet_instructions_refused"
	// FleetInstructionsArchived and FleetInstructionsUnarchived are an owner
	// hiding a version, or a whole name, from the board's list, or restoring it.
	FleetInstructionsArchived   Action = "fleet_instructions_archived"
	FleetInstructionsUnarchived Action = "fleet_instructions_unarchived"
	// VariantVersionSaved is an owner saving a variant version, and
	// VariantArchived and VariantUnarchived hiding or restoring one (or a
	// whole name). A variant decides what agents run on, so each is on
	// record. VariantRefused is anyone else calling a variant route.
	VariantVersionSaved Action = "variant_version_saved"
	VariantArchived     Action = "variant_archived"
	VariantUnarchived   Action = "variant_unarchived"
	VariantRefused      Action = "variant_refused"
	// AgentVariantChanged is an owner putting an agent on a variant version,
	// moving it to another or taking it off: what the agent runs on, so on
	// record with the version before and after.
	AgentVariantChanged Action = "agent_variant_changed"
	// AttemptVariantChanged is a member routing a ticket's attempt to a
	// variant version, moving it or clearing it: which agents will claim it,
	// so on record with the version before and after.
	AttemptVariantChanged Action = "attempt_variant_changed"
	// AttemptAdded is a member adding an attempt to a ticket: it can start
	// work, and can take a promotion away, so on record with the number,
	// whether it is a shadow and whether the promoted attempt lost its
	// promotion.
	AttemptAdded Action = "attempt_added"
	// AttemptClosed is a member closing a ticket's attempt: it ends the
	// attempt's agent's lease, so on record with the number and the status
	// the attempt was in.
	AttemptClosed Action = "attempt_closed"
	// AttemptPromoted is a member choosing which attempt of a ticket may reach
	// the base branch. It moves scope and the pull requests' targets, so it
	// is on record with the attempt, the one it replaced (0 for none) and the
	// status it was promoted from.
	AttemptPromoted Action = "attempt_promoted"
	// AttemptVerdictSet is a member giving a finished attempt their verdict,
	// or changing it: human judgement the later comparisons read, so on
	// record with the verdict, the one it replaced ("" for none) and whether
	// a note came with it.
	AttemptVerdictSet Action = "attempt_verdict_set"
	// SettingChanged is a person changing a tenant-wide setting, such as the
	// switch that stops the fleet starting new work, with the value before
	// and after. A write that changes nothing is not recorded.
	SettingChanged Action = "setting_changed"
	// AttachmentUploaded and AttachmentDeleted are a ticket attachment (or a
	// conversation's staged upload) being stored or removed: they name the
	// uploader or deleter, the attachment, its ticket or conversation, its
	// type and size. The file's name is not recorded.
	AttachmentUploaded Action = "attachment_uploaded"
	AttachmentDeleted  Action = "attachment_deleted"
	// CrossOriginRefused and CSRFRefused are browser writes refused by
	// authn.BrowserGuard; go-auth records them.
	CrossOriginRefused = goaudit.CrossOriginRefused
	CSRFRefused        = goaudit.CSRFRefused
)

// Actions is every Action, for pre-creating their series.
func Actions() []string {
	all := []Action{
		SignIn, SignInRefused, Reauth, ReauthRefused, InviteAccepted, InviteRefused, TenantRenamed, TenantRenameRefused, TenantCreated, TenantCreateRefused, TenantRecoloured, TenantRecolourRefused, TenantArchived, TenantUnarchived, TenantArchiveRefused, IdentityLinked, LinkRefused,
		PasswordAdded, PasswordChanged, SignInMethodRemoved,
		TokenReuse, ClientRegistered, ClientRegistrationRefused, AuthorizationApproved, AuthorizationDenied,
		DeviceStarted, DeviceApproved, DeviceDenied, GrantRevoked,
		ReadOnlyClientCreated, ReadOnlyClientRevoked, ClientSecretRefused, ReadOnlyRefused, SupervisorRouteRefused,
		AdminRefused, HarnessConfigChanged, ConsoleToolRefused, SecretWriteRefused,
		FleetInstructionsVersionSaved, FleetInstructionsDefaultChanged, FleetInstructionsRefused,
		FleetInstructionsArchived, FleetInstructionsUnarchived,
		VariantVersionSaved, VariantArchived, VariantUnarchived, VariantRefused, AgentVariantChanged, AttemptVariantChanged, AttemptAdded, AttemptClosed, AttemptPromoted, AttemptVerdictSet, SettingChanged, AttachmentUploaded, AttachmentDeleted,
		ProposalCreated, ProposalApproved, ProposalRejected, ProposalRefused, StewardVetoed, StewardUndone, ManifestChanged, BaseBranchChanged, RepoMoved, RepoSynced,
		IntakeSourceCreated, IntakeSourceRotated, IntakeSourceRevoked, IntakeDowngraded, IntakeRateLimited, IntakeResolveClosesEnabled, IntakeResolveClosesRefused,
		CodeHostCredentialConnected, CodeHostCredentialRemoved, CodeHostWebhookRotated,
		OwnerBoundBlockerChanged, OwnerBoundBlockerRefused,
		ChangeOpened, ChangeMarkedReady, ChangeChecksRead, ChangeMerged, ChangeReviewRequested, ChangeMergeRefused, ChangeOperationFailed, AttemptBranchCreated,
		ChangeClosedByRule, BaseBranchDeleted, ChangeMergedIntoBase, TicketClosedNoChange, TicketClosedAsMerged, LandingRequeueRefused, BatchTreeMismatch,
	}
	all = append(all, goaudit.CoreActions()...)
	names := make([]string, len(all))
	for i, a := range all {
		names[i] = string(a)
	}
	return names
}

// warnings are logged at warn rather than info: each one means something
// is wrong, not just that something happened. go-auth's own refusals
// (CrossOriginRefused, CSRFRefused) are warnings without being listed.
var warnings = []Action{
	TokenReuse,
	ClientRegistrationRefused,
	AdminRefused,
	ClientSecretRefused,
	ReadOnlyRefused,
	SupervisorRouteRefused,
	ConsoleToolRefused,
	SecretWriteRefused,
	OwnerBoundBlockerRefused,
	IntakeRateLimited,
	IntakeResolveClosesRefused,
	FleetInstructionsRefused,
	VariantRefused,
	ChangeMergeRefused,
	ChangeOperationFailed,
	LandingRequeueRefused,
	BatchTreeMismatch,
}

// New records to logHandler, counting with counter; nil counts nothing.
func New(logHandler slog.Handler, counter Counter) *Recorder {
	return goaudit.New(&goaudit.Config{Logger: logHandler, Counter: counter, Warnings: warnings})
}
