package prune

import (
	"time"

	"github.com/lowaa/slop-factory/go-privacy/retention"
)

// Inventory retention classes the rules enforce (privacy/inventory.yaml).
const (
	classAgentActivity           = "agent_activity"
	classLLMUsage                = "llm_usage"
	classGitHubDeliveries        = "github_deliveries"
	classConversationTranscripts = "conversation_transcripts"
	classExpiredCredentials      = "expired_credentials"
	// classPendingActions is not an inventory class: the inventory files
	// pending_actions under work_record, which is never pruned, so the
	// decided-proposal retention has no class period to be checked against.
	classPendingActions = "pending_actions"
)

// Rule is a retention.Rule with where it runs and which Result counter its
// deletions go to.
type Rule struct {
	retention.Rule
	// Global rules run once, outside any tenant; the rest run in every
	// tenant's own scope.
	Global bool
	count  func(*Result) *int64
}

// Rules is every delete-by-age rule the config enables.
//
// Order matters only within the credentials: grants go before the clients
// whose "never authorized" test reads them.
func Rules(cfg *Config) []*Rule {
	var rules []*Rule
	add := func(period time.Duration, count func(*Result) *int64, global bool, rs ...retention.Rule) {
		if period <= 0 {
			return
		}
		for _, r := range rs {
			r.Period = period
			rules = append(rules, &Rule{Rule: r, Global: global, count: count})
		}
	}
	sessions := func(r *Result) *int64 { return &r.Sessions }
	leases := func(r *Result) *int64 { return &r.Leases }
	activity := func(r *Result) *int64 { return &r.LeaseActivity }
	deliveries := func(r *Result) *int64 { return &r.Deliveries }
	usage := func(r *Result) *int64 { return &r.LLMUsage }
	pending := func(r *Result) *int64 { return &r.PendingActions }
	conversations := func(r *Result) *int64 { return &r.Conversations }
	credentials := func(r *Result) *int64 { return &r.Credentials }

	// A session with no ended_at is the agent's current run: never pruned
	// however old, and the NULL never compares as expired. Leases that
	// recorded a pruned session keep their row with a null reference.
	add(cfg.SessionRetention, sessions, false,
		retention.Rule{Class: classAgentActivity, Table: "sessions", TimeColumn: "ended_at"})

	// An unreleased lease is live or pinned by the integration lock, and
	// deleting it would cascade away the lock row the pin protects.
	add(cfg.LeaseRetention, leases, false,
		retention.Rule{Class: classAgentActivity, Table: "leases", TimeColumn: "released_at"})

	// Lease and lock history, on the lease clock: these rows describe the
	// same claims and locks. A row still open is never deleted.
	add(cfg.LeaseRetention, activity, false,
		retention.Rule{Class: classAgentActivity, Table: "scope_blocks", TimeColumn: "ended_at"},
		retention.Rule{Class: classAgentActivity, Table: "scope_extension_denials", TimeColumn: "resolved_at"},
		// An open denial is the agent's lease still running, or the agent having
		// given up. Once the lease it was refused under (same agent, acquired
		// before the denial) has been released, a later claim of the ticket
		// cannot resolve it, so it is dead by its age.
		retention.Rule{Class: classAgentActivity, Table: "scope_extension_denials", TimeColumn: "denied_at",
			Where: `resolved_at IS NULL AND NOT EXISTS (
				SELECT 1 FROM leases l WHERE l.tenant_id = scope_extension_denials.tenant_id
				   AND l.ticket_id = scope_extension_denials.ticket_id AND l.agent_id = scope_extension_denials.agent_id
				   AND l.acquired_at <= scope_extension_denials.denied_at AND l.released_at IS NULL)`},
		retention.Rule{Class: classAgentActivity, Table: "scope_narrowings", TimeColumn: "narrowed_at"},
		// An unreleased hold is the integration lock as it stands.
		retention.Rule{Class: classAgentActivity, Table: "lock_holds", TimeColumn: "released_at"})

	// A redelivery of one pruned is handled as new, which the ticket rules
	// make harmless: a GitHub review is refused once the ticket has left
	// review, and a stale one by its submission time; an intake event only
	// adds to the key's ticket again.
	add(cfg.DeliveryRetention, deliveries, false,
		retention.Rule{Class: classGitHubDeliveries, Table: "github_deliveries", TimeColumn: "received_at"},
		retention.Rule{Class: classGitHubDeliveries, Table: "intake_deliveries", TimeColumn: "received_at"})

	// A transcript's rows are all rewritten by each report, so they go
	// together. The per-day rollup goes by day: a day that ends after the
	// cutoff stays whole. A declaration a later one of the same scope has
	// superseded goes; the latest of a scope is what its next report is
	// split by, however old.
	add(cfg.LLMUsageRetention, usage, false,
		retention.Rule{Class: classLLMUsage, Table: "llm_usage", TimeColumn: "updated_at"},
		retention.Rule{Class: classLLMUsage, Table: "llm_usage_marks", TimeColumn: "updated_at"},
		retention.Rule{Class: classLLMUsage, Table: "llm_guidance_usage", TimeColumn: "updated_at"},
		retention.Rule{Class: classLLMUsage, Table: "llm_usage_daily", TimeColumn: "day",
			Where: `"day" + interval '1 day' <= $1`},
		retention.Rule{Class: classLLMUsage, Table: "usage_declarations", TimeColumn: "declared_at",
			Where: `EXISTS (SELECT 1 FROM usage_declarations n
				WHERE n.tenant_id = usage_declarations.tenant_id AND n.agent_id = usage_declarations.agent_id
				  AND n.conversation_id IS NOT DISTINCT FROM usage_declarations.conversation_id
				  AND n.id > usage_declarations.id)`})

	// A pending proposal is never deleted. Expiring undecided ones is not a
	// rule: it updates (see Service).
	add(cfg.PendingActionRetention, pending, false,
		retention.Rule{Class: classPendingActions, Table: "pending_actions", TimeColumn: "decided_at", Where: `status <> 'pending'`})

	// A live conversation is never deleted, and neither is one with work the
	// host has not acknowledged, so a host that comes back is still told (a
	// stop, above all) rather than finding it deleted.
	add(cfg.ConversationRetention, conversations, false,
		retention.Rule{Class: classConversationTranscripts, Table: "conductor_conversations", TimeColumn: "updated_at",
			Where: `state IN ('failed', 'stopped') AND NOT EXISTS (
				SELECT 1 FROM conductor_work w
				 WHERE w.tenant_id = conductor_conversations.tenant_id AND w.conversation_id = conductor_conversations.id)`})

	// Not zero: a rotated refresh token presented again is recognised as a
	// stolen copy -- and its grant revoked -- only while its row still
	// exists, so a credential outlives its expiry a little. Accepted
	// invitations stay as the record of who invited whom. Grants do not
	// expire, so they are dead once revoked.
	add(ExpiredCredentialGrace, credentials, true,
		retention.Rule{Class: classExpiredCredentials, Table: "global_auth_tokens", TimeColumn: "expires_at"},
		retention.Rule{Class: classExpiredCredentials, Table: "global_invites", TimeColumn: "expires_at", Where: `accepted_at IS NULL`},
		retention.Rule{Class: classExpiredCredentials, Table: "global_invites", TimeColumn: "revoked_at", Where: `accepted_at IS NULL`},
		retention.Rule{Class: classExpiredCredentials, Table: "global_oauth_auth_requests", TimeColumn: "expires_at"},
		retention.Rule{Class: classExpiredCredentials, Table: "global_oauth_codes", TimeColumn: "expires_at"},
		retention.Rule{Class: classExpiredCredentials, Table: "global_oauth_device_codes", TimeColumn: "expires_at"},
		retention.Rule{Class: classExpiredCredentials, Table: "global_oauth_tokens", TimeColumn: "expires_at"},
		// Its tokens and codes cascade, and are mostly gone by now.
		retention.Rule{Class: classExpiredCredentials, Table: "global_oauth_grants", TimeColumn: "revoked_at"})

	// A client registered through the open registration endpoint that never
	// completed an authorization is dead weight, and nothing else ever
	// removes one. Clients with a grant, and the seeded workspace client,
	// stay. It goes after an hour, not the credentials' day: Claude Code
	// registers and authorizes in one go.
	add(UnclaimedClientGrace, credentials, true,
		retention.Rule{Class: classExpiredCredentials, Table: "global_oauth_clients", TimeColumn: "created_at",
			Where: `id <> 'slop-haus-workspace' AND NOT EXISTS (
				SELECT 1 FROM global_oauth_grants g WHERE g.client_id = global_oauth_clients.id)`})

	return rules
}
