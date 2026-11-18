import type { components, paths } from "@/api_generated/schema";

type Schemas = components["schemas"];

export type Priority = Schemas["Priority"];
export type Status = Schemas["Status"];

// RoleMatch says how many of a ticket's required roles the claiming agent
// needs: one of them (any) or every one (all).
export type RoleMatch = Schemas["TicketBody"]["role_match"];

export type TicketPath = Schemas["PathBody"];

// TicketRef names another ticket: enough to show which one is meant
// without loading it, and its status -- so a ticket stuck on a parked
// blocker says so.
export type TicketRef = Schemas["TicketRefBody"];

// RoleRef names one of a ticket's required roles -- id and name only.
// Deliberately not AgentRole: a ticket's requirement never carries a
// description, so there's nothing here for a caller to accidentally read
// stale.
export type RoleRef = Schemas["RoleRefBody"];

// BlockerList is what adding or removing a blocker answers with: the
// ticket's resulting blockers, and its status (with that status's moves),
// which the change can move between open and blocked.
export type BlockerList = Schemas["BlockerListResponseBody"];

// Ticket is server/internal/ticket/wire.go's TicketBody, whose comments
// say what each field means. allowed_moves and can_ready are the server's
// word on what a person may do to the status: follow them rather than
// keeping a copy of the rules here.
export type Ticket = Schemas["TicketBody"];

// TicketAttempt is one attempt of a ticket, as the ticket's detail carries
// them (and only the detail: board and list rows leave them out).
// AttemptCard is what a board card of one attempt of a ticket with several
// carries beyond the ticket's own fields.
export type AttemptCard = NonNullable<Ticket["attempt"]>;
export type TicketAttempt = NonNullable<Ticket["attempts"]>[number];

// AttemptVerdict is a person's verdict on a finished attempt, with the ones
// it replaced.
export type AttemptVerdict = Schemas["AttemptVerdictBody"];
export type AttemptVerdictValue = AttemptVerdict["verdict"];

// MergeFacts is what the code host says about a ticket's change, for an owner
// deciding it was merged outside slop-haus; CloseAsMergedResult is the answer
// to closing on it (see server/internal/ticket/close_merged_handler.go).
export type MergeFacts = Schemas["MergeFactsBody"];
export type CloseAsMergedResult = Schemas["CloseTicketAsMergedResultBody"];

// VariantOption is a variant version a ticket can be routed to.
export type VariantOption = Schemas["VariantOptionBody"];

export type Comment = Schemas["CommentBody"];

// Attachment is an image or video on a ticket (or staged in a conversation).
export type Attachment = Schemas["AttachmentBody"];

// AgentContext is a handoff -- what an agent leaves on a ticket when it
// stops for a discussion, so the next agent to pick it up (itself after a
// /clear, or another one) can carry on from it.
export type AgentContext = Schemas["AgentContextBody"];

// IntegrationLock is who holds the merge lock and who is queued for it:
// the server's figures, durations by its clock, not the browser's.
export type IntegrationLock = Schemas["IntegrationLockStateResponseBody"];
// QueueBlockage is the queue waiting on path scopes held elsewhere: work
// idle agents could take, each ticket of it colliding with a held scope.
export type QueueBlockage = Schemas["QueueBlockageBody"];
export type NamedIntegrationLock = Schemas["NamedLockBody"];

// Lease is the ticket an agent currently holds, if any.
export type Lease = Schemas["LeaseBody"];

// AgentRole is one role a tenant can hand to any of its agents, deciding
// which tickets they may claim.
export type AgentRole = Schemas["RoleBody"];

// Agent has deliberately no token field: an agent has no static credential,
// only whatever OAuth grants a client holds for it (see Grant below).
export type Agent = Schemas["AgentBody"];

// Conductor is an agent kind owned by the person who registered it: only they
// may use it (owned_by_me), and a team's owner may disable it but not use it.
// Like an agent it has no token of its own.
export type Conductor = Schemas["ConductorBody"];
// ConductorDirectoryEntry is what any member of the team may see of a
// conductor: its owner, when it last connected and whether a session is live
// on it. Nothing about the sessions themselves.
export type ConductorDirectoryEntry = Schemas["ConductorDirectoryEntryBody"];
// ConductorList is the signed-in person's own conductors and, for a team's
// owner, everyone else's, which they can only disable.
export interface ConductorList {
  own: Conductor[];
  others: Conductor[];
}

// Conversation is one Claude Code session on a remote control (a machine of
// the person's own), driven from the browser. ConversationDetail is what
// reading one adds: whether the machine's host is there, the working copy's
// state and the scopes agents hold that its changes lie in.
export type Conversation = Schemas["ConversationBody"];
export type ConversationDetail = Schemas["ConversationDetailBody"];
export type ConversationEvent = Schemas["EventBody"];

export interface ConversationEventsPage {
  events: ConversationEvent[];
  // Older events exist, to be read with before = nextBefore.
  hasMore: boolean;
  nextBefore: number;
  // The owner's recent prompts, oldest first; only on the first read.
  prompts: string[] | null;
}
// RepoStateBody is where the working copy stands after a turn, as the host
// reported it: shown, never relied on.
export type RepoStateBody = Schemas["RepoStateBody"];
export type HeldScopeOverlap = Schemas["HeldScopeOverlapBody"];
// ConductorHost is what the Remote Control page shows of a machine's host:
// whether it is there.
export type ConductorHost = Schemas["HostBody"];

// Workspace is the tenant's manifest (null until an owner adopts one) with
// each agent's layout and how it compares. Layouts are self-asserted by the
// agent's machine: shown to a human, compared by the server, never trusted.
export type Workspace = Schemas["WorkspaceBody"];
export type WorkspaceAgent = Schemas["AgentWorkspaceBody"];
export type ManifestRepo = Schemas["Repo"];
export type BaseBranchResult = Schemas["BaseBranchResult"];
export type SavedWorkspaceManifest = Schemas["SetManifestBody"];
export type WorkspaceProposal = Schemas["Proposal"];
export type RepoMove = Schemas["MoveRepoResponseBody"];
export type BranchDivergence = Schemas["Divergence"];
export type RepoSync = Schemas["SyncResult"];

// FleetInstructions is the tenant's named, immutable versions of the text put
// in every agent's briefing, and which version agents are given (null: none).
// The text is an owner's, shown as plain text, never as markup.
export type FleetInstructions = Schemas["FleetInstructionsBody"];
export type FleetInstructionsVersion = Schemas["VersionBody"];
export type FleetInstructionsKind = FleetInstructionsVersion["kind"];
export type BriefingFile = Schemas["BriefingFile"];

// A variant is a named, immutable version of a harness, its settings and a
// pinned fleet instructions version (null: none, never the tenant's default).
// An agent on one runs on its settings alone. Owners only.
export type VariantVersion = Schemas["VariantVersionBody"];

// Proposal is a human action a console agent has proposed for the signed-in
// person to approve (server/internal/pendingaction). action_line is what
// approving does, written by the server; text is the agent's own words and
// is untrusted -- render it as plain text, never as markup.
export type Proposal = Schemas["ProposalBody"];
export type ProposalState = Proposal["state"];
// The steward's views (server/internal/steward): the digest of what it did
// over a window, and the notices of what the owner may still veto or undo.
export type StewardDigest = Schemas["StewardDigestResponseBody"];
export type StewardDigestCategory = Schemas["DigestCategoryBody"];
export type StewardDiscussions = Schemas["DiscussionDigestBody"];
export type StewardDiscussionScore = Schemas["DiscussionScoreBody"];
export type StewardDigestWindow = "24h" | "7d";
// The steward's label and draft for one discussion round of an attempt
// (server/internal/steward/discussionanswer), addressed by DiscussionRound.
export type DiscussionDraft = Schemas["DiscussionDraftBody"];
export type DiscussionLabel = Schemas["DiscussionLabelBody"];
export type StewardApply = Schemas["StewardApplyBody"];
export type DiscussionDecision = Schemas["DiscussionDecisionBody"];
export type DiscussionRound = { ticket_id: number; attempt: number; round: number };
export type StewardNotices = Schemas["StewardNoticesResponseBody"];
export type StewardNotice = Schemas["NoticeBody"];
export type DecidedProposalState = Exclude<ProposalState, "pending">;

// AgentMessage is something the signed-in person said to one agent: pending
// until the agent's next call to slop-haus carries it, then delivered, or dropped
// if the agent's hold on the ticket ended first. There
// is no read state; the server can't know.
export type AgentMessage = Schemas["AgentMessageBody"];

// ProposalResult is how one proposal of a batch ended.
export type ProposalResult = Schemas["ResultBody"];

// The kinds an approve-all takes: never a review decision or a status move.
export type BatchProposalKind = "ready" | "discussion_answer";

// AgentSession is an agent's current run. The harness, model and labels
// are the agent's machine's own say-so: shown to a human, never trusted
// for anything. Any of them may be empty.
export type AgentSession = Schemas["SessionBody"];

// ConfigStatus compares the harness config revision a run started with to
// the agent's own: stale means the run needs a restart to pick up the
// current config.
export type ConfigStatus = AgentSession["config_status"];

// -- Harness config --
//
// Settings are keyed harness, then setting name; a setting that is absent
// is unset.

export type HarnessSettings = Schemas["ScopeBody"]["settings"];
export type HarnessSettingDefinition = Schemas["DefinitionBody"];

// HarnessConfigScope is one saved scope: agent_id is null for the tenant's
// defaults.
export type HarnessConfigScope = Schemas["ScopeBody"];

// HarnessConfig's defaults are null until they are first saved.
export type HarnessConfig = Schemas["GetHarnessConfigResponseBody"];

// SupervisorStatus: offline is true once a supervisor that has polled before
// has been quiet for five minutes; a tenant that never ran one is not offline.
export type SupervisorStatus = Schemas["StatusBody"];

// -- OAuth --
//
// A signed-in user is shown a pending authorization (the consent page, or
// a device code someone typed in) and decides which agent it acts as; the
// client never sees a code, a device code or a token here -- those go
// straight from the browser's redirect (or a workspace's own curl) to the
// OAuth token endpoint, never through this JSON API.

// PendingAuthorization is what the consent page (/authorize?request=<id>)
// shows before the user decides. redirect_host is always a loopback
// address -- shown so the page can say "a program on your own computer".
export type PendingAuthorization = Schemas["PendingAuthResponseBody"];

// PendingDeviceAuthorization is what the device page shows once a user
// code resolves to a real, unexpired authorization.
export type PendingDeviceAuthorization = Schemas["PendingDeviceResponseBody"];

type DecisionBody = Schemas["DecisionBody"];

// DecisionInput is approve naming exactly one of an existing agent (by id)
// or a new one (by name), or a refusal -- narrower than oauth.DecisionBody,
// which has every field optional and leaves the rule to the server. Shared
// by the consent and device decision calls below.
export type DecisionInput =
  | { approve: true; agent_id: NonNullable<DecisionBody["agent_id"]> }
  | {
      approve: true;
      agent_name: NonNullable<DecisionBody["agent_name"]>;
      agent_kind?: DecisionBody["agent_kind"];
      // The new agent takes over from this one, which is disabled and archived.
      replaces_agent_id?: DecisionBody["replaces_agent_id"];
      // Only for a supervisor: how many workers it may run at once.
      slot_cap?: DecisionBody["slot_cap"];
    }
  | { approve: false };

// RedirectTo is where the consent page's decision sends the browser next,
// back to the client that started the authorization.
export type RedirectTo = Schemas["RedirectResponseBody"];

// Grant is one client authorized to act as one agent. There is
// deliberately no token or secret here: a grant is revoked by id, never
// re-displayed.
export type Grant = Schemas["GrantBody"];

// Profile is the signed-in person, and the tenant everything on the board
// belongs to. is_admin grants nothing on its own -- the server's
// AdminCheck (and every /api/admin/* route) is what enforces it. The
// session's CSRF token is SessionClient's to keep, not the app's.
export type Profile = Omit<Schemas["ProfileBody"], "csrf_token">;

// Member is another person in the same tenant -- enough to put a name to
// their comments.
export type Member = Schemas["MemberBody"];

// LoginProviderBody is a login provider people may sign in with besides a
// password.
export type LoginProviderBody = Schemas["ProviderBody"];

// AuthConfig is what the sign-in page should offer.
export type AuthConfig = Schemas["AuthConfigResponseBody"];

// SignInMethodsBody is the signed-in account's ways of signing in.
export type SignInMethodsBody = Schemas["SignInMethodsResponseBody"];

// InviteKind: an admin's invite starts the invitee their own tenant, a
// member's invite brings them into the inviter's.
export type InviteKind = Schemas["InviteBody"]["kind"];

// Invite's tenant_id is null for an admin's invite (there is no tenant
// yet).
export type Invite = Schemas["InviteBody"];

// InvitePreview is what the invite link's own page (before there is a
// session, and possibly before there is an account at all) is allowed to
// know about the invite it names.
export type InvitePreview = Schemas["InvitePreviewResponseBody"];

// Tenant is one of the tenants the signed-in user belongs to, for the
// tenant switcher.
export type Tenant = Schemas["TenantBody"];

export interface TenantOverview {
  tenants: Tenant[];
  ownerLimit: number;
}

// -- GitHub webhooks --

// GitHubDelivery is one webhook call GitHub made, and what it did.
export type GitHubDelivery = Schemas["DeliveryBody"];
export type GitHubWebhooks = Schemas["WebhooksResponseBody"];


// -- Code host credential --

// GitHubAppStatus is whether a GitHub App is connected for the tenant, and
// which. The private key is write-only: nothing the server answers carries it.
export type GitHubAppStatus = Schemas["GitHubAppStatusBody"];
export type GitHubAppConnection = Schemas["GitHubAppBody"];
export type GitHubAppVerification = Schemas["GitHubAppVerifyResponseBody"];
export type GitHubAppManifestStart = Schemas["GitHubAppManifestResponseBody"];
export type GitHubAppCreated = Schemas["GitHubAppCreatedResponseBody"];
export type GitHubAppInstalled = Schemas["GitHubAppInstalledResponseBody"];
export type GitHubAppInstalledRequest =
  Schemas["GitHubAppInstalledRequestBody"];
// GitHubAppWebhook is whether the App's own webhook is delivering, and what
// is left to do if not. It carries no secret.
export type GitHubAppWebhook = Schemas["GitHubAppWebhookBody"];
export type GitHubAppWebhookCheck = Schemas["GitHubAppWebhookCheckResponseBody"];
export type ConnectGitHubApp =
  paths["/api/codehost/github-app"]["put"]["requestBody"]["content"]["application/json"];

// -- Intake --

// IntakeSource is one external system a tenant lets open, update and resolve
// tickets through the intake door.
export type IntakeSource = Schemas["IntakeSourceBody"];
export type IntakeDelivery = Schemas["IntakeDeliveryBody"];
export type IntakeSources = Schemas["IntakeSourcesResponseBody"];
export type CreateIntakeSource = Schemas["CreateIntakeSourceRequestBody"];

// IntakeCredential is a source with its credential, answered once: the bearer
// token or HMAC secret is shown on creation and rotation and never again.
export type IntakeCredential = Schemas["IntakeCredentialBody"];

// ExternalEvent is one thing an intake source sent about a ticket. Every text
// field of it was written by the sender: show it as external content only.
export type ExternalEvent = Schemas["ExternalEventBody"];

// BoardChange is the board stream's one event: the board's new revision, and
// either the tickets that changed since the last one or reload: true when it
// cannot say. A server from before the tickets sends neither.
export type BoardChange = Schemas["BoardChangeBody"];

// Setting mirrors the server's thin settings table rather than collapsing
// to one polymorphic "value": the field named by `type` is the live one;
// the rest are null. bool_val is the server's own derivation from int_val
// -- a toggle should not have to know a bool is stored as a 0 or a 1.
export type Setting = Schemas["SettingBody"];
export type SettingType = Setting["type"];

// TICKET_PROCESSING_ENABLED is the one setting the UI currently drives.
// The name is the server's (settings.TicketProcessingEnabled) and is the
// key in the settings table, not a display label.
export const TICKET_PROCESSING_ENABLED = "ticket_processing_enabled";

// UserSetting is one of the signed-in person's own preferences (not the
// server-wide Setting above): the value that applies to them now, or the
// default while they have never set it. They follow the person into every
// tenant.
export type UserSetting = Schemas["UserSettingBody"];

// REMOTE_CONTROL_SESSION_SWITCH_EXPLAINED is the server's
// usersettings.RemoteControlSessionSwitchExplained.
export const REMOTE_CONTROL_SESSION_SWITCH_EXPLAINED =
  "remote_control.session_switch_explained";

// A TenantUserSetting is the same shape, but its value applies only in the
// tenant the person is signed into.
export type TenantUserSetting = UserSetting;
export type TenantSettingChange = Schemas["SettingChangeBody"];
export type TenantSettingHistory = Schemas["SettingHistoryResponseBody"];

// A ServerSettingChange is one change to a tenant-wide Setting. Who made it
// is one of three cases the page must keep apart: a person (changed_by_id,
// named by changed_by_name when they set one), a removed account
// (changed_by_deleted), or a change from before history was kept
// (before_history_kept), which is not a deleted user.
export type ServerSettingChange = Schemas["ServerSettingChangeBody"];
export type ServerSettingHistory = Schemas["ServerSettingHistoryResponseBody"];

type UpdateSettingBody = Schemas["UpdateSettingBody"];

// UpdateSettingInput states the type it believes it is writing, which the
// server checks against the setting's declared kind -- a client that has
// drifted gets a 422 naming both kinds rather than a silently ignored
// write. Narrower than UpdateSettingBody, whose value fields are all
// optional: each type carries its own field.
export type UpdateSettingInput =
  | {
      type: Extract<SettingType, "bool">;
      bool_val: NonNullable<UpdateSettingBody["bool_val"]>;
    }
  | {
      type: Extract<SettingType, "string">;
      string_val: NonNullable<UpdateSettingBody["string_val"]>;
    }
  | {
      type: Extract<SettingType, "int">;
      int_val: NonNullable<UpdateSettingBody["int_val"]>;
    }
  | {
      type: Extract<SettingType, "float">;
      float_val: NonNullable<UpdateSettingBody["float_val"]>;
    };

// PathInput carries no source: which kind a scope is, is the server's to
// decide -- one supplied when filing or editing is "filed", one granted by
// a scope extension is "extended".
export type PathInput = Schemas["PathInput"];

// UpdateAgentRoleInput carries only the fields a caller wants changed.
export type UpdateAgentRoleInput = Schemas["UpdateAgentRoleBody"];

// CreateTicketInput's status, omitted, means "open", what this route did
// before Preparing existed; the board's own form always sends one -- see
// new_ticket_form.tsx. role_match omitted means "any"; see setTicketRoles
// for changing the requirement afterwards.
export type CreateTicketInput = Schemas["CreateTicketBody"];

// UpdateTicketInput carries only the fields a caller wants changed -- an
// omitted field means "leave alone". Title and body are not here: they
// change through editTicket, which records every edit in the ticket's
// history.
export type UpdateTicketInput = Schemas["UpdateTicketBody"];

// EditTicketInput's base_revision is the ticket's revision when the editor
// was opened: an edit made by someone else since is refused with a 409
// rather than silently overwritten.
export type EditTicketInput = Schemas["CreateTicketRevisionBody"];

export type DiffLine = Schemas["DiffLineBody"];

// Revision is one edit to a ticket's title or body. Each changed field
// carries its full old and new text; an untouched one is null on both
// sides. body_diff is the server's line diff of old_body against new_body,
// worked out on read.
export type Revision = Schemas["RevisionBody"];

// ReviewRun is one slop-review run on an attempt's branch, posted by the supervisor
// that ran it for the agent or by the agent itself; a finding is what it
// reported, with the label an agent gave it after checking.
export type ReviewRun = Schemas["ReviewRunBody"];
export type ReviewFinding = Schemas["ReviewFindingBody"];

// StatusChange is one move of a ticket's status, oldest first from the
// server. The ticket's own status: with several attempts it is worked out
// from all of them, so a change names no attempt. cause is already parsed
// by the server (kind "proposal" with proposal_id/auto_approved, or "other"
// with its text), so no caller splits the recorded string.
export type StatusChange = Schemas["StatusChangeBody"];

// GraphNode/GraphEdge/BlockingGraph are server/internal/ticket/graph.go's
// Graph. "blocked_by" is the same ticket_deps edge as Ticket's own
// depends_on -- this is a read-only *view* over that data (layers,
// transitive blockers, unblocks counts already worked out), not a second
// dependency concept, so there is deliberately no client-side re-derivation
// of any of it.
export type GraphNode = Schemas["GraphNode"];

// GraphEdge is named from the reader's point of view: blocker holds up
// blocked.
export type GraphEdge = Schemas["GraphEdge"];

// BlockingGraph's layers list node ids by depth -- a topological order the
// server has already computed, so a caller lays the graph out in columns
// rather than walking edges itself.
export type BlockingGraph = Schemas["Graph"];

export type Report = Schemas["ReportBody"];
export type ReportState = Schemas["StateTimeBody"];
export type ReportLead = Schemas["LeadTimeBody"];
export type ReportWaitingPage = Schemas["WaitingPageBody"];
export type ReportWaitingTicket = Schemas["WaitingTicketBody"];
export type ReportQuery = NonNullable<
  paths["/api/reports"]["get"]["parameters"]["query"]
>;
export type LlmUsageReport = Schemas["UsageReportBody"];
export type LlmUsageSums = Schemas["UsageSumsBody"];
export type LlmUsageTicket = Schemas["UsageTicketBody"];
export type LlmUsageTicketPage = Schemas["UsageTicketPageBody"];
export type ScopeCostReport = Schemas["ScopeCostBody"];
export type ScopeCostQuery = NonNullable<
  paths["/api/reports/scope-cost"]["get"]["parameters"]["query"]
>;
export type LockHistoryReport = Schemas["LockHistoryBody"];
export type LockHistoryQuery = NonNullable<
  paths["/api/reports/lock-history"]["get"]["parameters"]["query"]
>;
export type ShadowDecisionsReport = Schemas["ShadowDecisionsBody"];
export type ShadowDecisionsQuery = NonNullable<
  paths["/api/reports/shadow-decisions"]["get"]["parameters"]["query"]
>;
export type TicketLlmUsage = Schemas["TicketUsageBody"];
export type LlmUsageQuery = NonNullable<
  paths["/api/reports/llm-usage"]["get"]["parameters"]["query"]
>;
// The ticket filter params of GET /api/proposals: the proposals of the
// tickets that match. State is the first argument of listProposals, not here.
export type ProposalTicketFilter = Omit<
  NonNullable<paths["/api/proposals"]["get"]["parameters"]["query"]>,
  "state" | "order" | "limit" | "cursor"
>;

// One page of proposals, oldest first when pending and newest first when decided. nextCursor is absent on the
// last page and valid only for the same filter.
export interface ProposalPage {
  proposals: Proposal[];
  nextCursor?: string;
}
export type BlockedTicketsQuery = NonNullable<
  paths["/api/tickets/blocked"]["get"]["parameters"]["query"]
>;
export type LlmUsageTicketsQuery = NonNullable<
  paths["/api/reports/llm-usage/tickets"]["get"]["parameters"]["query"]
>;
export type ReportWaitingQuery = NonNullable<
  paths["/api/reports/waiting"]["get"]["parameters"]["query"]
>;

// BlockerCandidate is a ticket that may be added as a blocker of another.
// The server decides what qualifies -- it excludes the ticket itself,
// anything waiting on it, whatever already blocks it and anything
// finished, so a picker built on this offers only edges the API will
// accept.
export type OnboardingStatus = Schemas["OnboardingStatusBody"];
export type BlockerCandidate = Schemas["BlockerCandidate"];

// RoleInUseTicket names a ticket that still requires a role a delete tried
// to remove -- mirrors role_handler.go's RoleInUseTicket.
export interface RoleInUseTicket {
  id: number;
  title: string;
  status: Status;
}

// BulkRefusal is one ticket a bulk action refused. status is null when the
// ticket was not found.
export type BulkRefusal = Schemas["BulkRefusalEntry"];

export interface SetupScript {
  filename: string;
  script: string;
}

// ListTicketsFilter mirrors server/internal/ticket/handler.go's
// ListTicketsRequest. awaitingPostReview narrows the list to done tickets
// that merged without anyone approving them and that nobody has marked
// reviewed since, most recently closed first. text (?q=) narrows it to
// tickets containing every word of it somewhere in the title, body,
// comments or handoffs.
export interface ListTicketsFilter {
  statuses?: Status[];
  awaitingPostReview?: boolean;
  text?: string;
  // Paging: UTC days (YYYY-MM-DD, inclusive) the ticket merged between, and
  // the opaque cursor a previous page returned.
  closedFrom?: string;
  closedTo?: string;
  cursor?: string;
  // One of the server's registered sorts; omitted is the list's default.
  sort?: string;
  // Page size; omitted is the server's default. The server caps it (200).
  limit?: number;
}

export interface TicketPage {
  tickets: Ticket[];
  // Absent on the last page.
  nextCursor?: string;
}
