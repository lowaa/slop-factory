package seeddata

import (
	"crypto/sha256"
	"time"

	"github.com/google/uuid"
)

// seededCredentialID is the same every round: the table has no one-per-tenant
// key to replace the last round's row by, and a binding is unique per tenant.
func seededCredentialID(tenant uuid.UUID) uuid.UUID {
	return uuid.NewSHA1(tenant, []byte("codehost credential"))
}

func init() {
	register(Registry{
		// Before the codehost_apps migration this table held the App too.
		"codehost_apps": tenantSeeder(func(s *seed) error {
			s.upsert("codehost_apps", "(tenant_id, id)", map[string]any{
				"tenant_id": s.tenant, "id": s.tenant.String(), "provider": "github_app", "app_id": 1,
				"private_key_enc": []byte("not a real blob"), "key_id": "0123456789abcdef", "key_fingerprint": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				"created_by": s.lastFor(s.tenant, "global_users").get("id"), "updated_at": s.at(0),
			})
			return nil
		}),
		"codehost_credentials": tenantSeederAfter(func(s *seed) error {
			if s.cols["codehost_apps"] == nil {
				s.upsert("codehost_credentials", "(tenant_id)", map[string]any{
					"tenant_id": s.tenant, "id": s.tenant.String(), "provider": "github_app", "app_id": 1, "installation_id": 2,
					"private_key_enc": []byte("not a real blob"), "key_id": "0123456789abcdef", "key_fingerprint": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
					"created_by": s.lastFor(s.tenant, "global_users").get("id"), "updated_at": s.at(0),
				})
				return nil
			}
			s.upsert("codehost_credentials", "(tenant_id, binding)", map[string]any{
				"tenant_id": s.tenant, "id": seededCredentialID(s.tenant), "app_ref": s.tenant.String(), "installation_id": 2,
				"binding": "github.com", "updated_at": s.at(0),
			})
			return nil
		}, "global_users"),
		"codehost_setup_states": tenantSeederAfter(func(s *seed) error {
			hash := sha256.Sum256([]byte(s.tag("state")))
			s.insert("codehost_setup_states", map[string]any{
				"token_hash": hash[:], "kind": "manifest",
				"user_id": s.lastFor(s.tenant, "global_users").get("id"), "expires_at": s.at(0),
			})
			return nil
		}, "global_users"),
		"ticket_changes": tenantSeeder(func(s *seed) error {
			s.insert("ticket_changes", map[string]any{
				"ticket_id": s.last("tickets").get("id"), "attempt_number": 1, "host": "github.com", "owner": "acme", "repo": "app", "number": 1 + s.round,
				"head_branch": s.tag("branch"), "base_branch": "main", "draft": false,
				"opened_by_agent_id": s.last("agents").get("id"), "opened_at": s.at(0),
			})
			return nil
		}),
		"merge_drift_decisions": tenantSeeder(func(s *seed) error {
			// One decided and one whose later checks were linked.
			change := s.last("ticket_changes")
			s.insert("merge_drift_decisions", map[string]any{
				"ticket_id": change.get("ticket_id"), "change_id": change.get("id"), "decision": "would_accept", "reason": "the base moved only in independent apps",
				"head_sha": s.tag("head"), "base_tip_sha": s.tag("tip"), "enforced": false, "decided_at": s.at(0),
			})
			s.insert("merge_drift_decisions", map[string]any{
				"ticket_id": change.get("ticket_id"), "change_id": change.get("id"), "decision": "would_refuse", "reason": "go.work is not in an independent app",
				"head_sha": s.tag("head2"), "base_tip_sha": s.tag("tip2"), "enforced": false, "decided_at": s.at(time.Hour),
				"ci_head_sha": s.tag("ci"), "ci_passed": false, "ci_recorded_at": s.at(2 * time.Hour),
			})
			return nil
		}),
		"server_merge_outcomes": tenantSeederAfter(func(s *seed) error {
			s.insert("server_merge_outcomes", map[string]any{
				"ticket_id": s.last("tickets").get("id"), "attempt_number": 1, "change_url": "https://github.com/acme/app/pull/" + s.tag("pr"),
				"head_sha": s.tag("head"), "outcome": "checks_red", "detail": "lint", "created_at": s.at(0),
				"approver_user_id": s.lastFor(s.tenant, "global_users").get("id"), "approved_at": s.at(0),
			})
			return nil
		}, "global_users"),
		"attempt_signals": tenantSeeder(func(s *seed) error {
			s.insert("attempt_signals", map[string]any{
				"ticket_id": s.last("tickets").get("id"), "attempt_number": 1, "computed_at": s.at(0),
				"lines_added": 3, "checks_passed": "yes", "detail": `{"final":false}`,
			})
			return nil
		}),
		// Handed back, not landing: the landing runner works any row in state
		// landing at start-up, which would change the seeded rows the upgrade check
		// compares. (The server agent it registers is seeded by upgrade_data.)
		"landings": tenantSeeder(func(s *seed) error {
			s.insert("landings", map[string]any{
				"ticket_id": s.last("tickets").get("id"), "attempt_number": 1, "change_url": "https://github.com/acme/app/pull/" + s.tag("pr"),
				"head_sha": s.tag("head"), "state": "back_with_agent", "queue_state": "back_with_agent", "back_reason": "checks_red", "enqueued_at": s.at(0), "updated_at": s.at(0),
			})
			return nil
		}),
		"batches": tenantSeeder(func(s *seed) error {
			s.insert("batches", map[string]any{
				"base_sha": s.tag("base"), "branch": "batch-" + s.tag("b"), "member_ticket_ids": []int64{1},
				"state": "done", "result": "voided", "created_at": s.at(0), "updated_at": s.at(0),
			})
			return nil
		}),
		"ignored_merges": tenantSeeder(func(s *seed) error {
			s.insert("ignored_merges", map[string]any{
				"ticket_id": s.last("tickets").get("id"), "attempt_number": 1, "host": "github.com", "owner": "acme", "repo": "app",
				"number": 1, "merge_sha": s.tag("merge"), "merger_login": "outsider", "merger_github_id": 4242,
				"reason": "the merger is not a member here with that GitHub account linked", "created_at": s.at(0),
			})
			return nil
		}),
	})
}
