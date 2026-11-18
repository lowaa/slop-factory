package seeddata

import "time"

func init() {
	register(Registry{
		"steward_leases": tenantSeeder(func(s *seed) error {
			s.upsert("steward_leases", "(tenant_id)", map[string]any{
				"holder_id": s.recent("agents", 1)[0].get("id"), "expires_at": s.at(time.Hour),
			})
			return nil
		}),
		"steward_assessments": tenantSeederAfter(func(s *seed) error {
			s.insert("steward_assessments", map[string]any{
				"id": s.id("assessment"), "ticket_id": s.recent("tickets", 2)[0].get("id"), "pending_action_id": s.id("pending"),
				"category": "merge_continue", "tier": "veto", "verdict": "would_veto", "mode": "live", "reason": "green",
				"evidence": `[{"check": "ci_green_on_head", "ok": true}]`, "ticket_revision": 1, "head_sha": s.tag("head"),
				"applied_at": s.at(0), "veto_deadline": s.at(time.Hour), "vetoed_at": s.at(time.Minute),
				"vetoed_by": s.lastFor(s.tenant, "global_users").get("id"),
				"agent_id":  s.recent("agents", 2)[0].get("id"), "created_at": s.at(0),
			})
			return nil
		}, "pending_actions", "global_users"),
		"steward_drafts": tenantSeederAfter(func(s *seed) error {
			s.insert("steward_drafts", map[string]any{
				"id": s.id("draft"), "ticket_id": s.recent("tickets", 2)[0].get("id"), "pending_action_id": s.id("pending"),
				"category": "merge_continue", "kind": "discussion_answer", "body": "carry on", "reason": "green",
				"evidence": `[{"check": "ci_green_on_head", "ok": true}]`, "ticket_revision": 1, "head_sha": s.tag("head"),
				"agent_id": s.recent("agents", 2)[0].get("id"), "created_at": s.at(0),
			})
			return nil
		}, "pending_actions"),
		"steward_discussion_labels": tenantSeeder(func(s *seed) error {
			s.insert("steward_discussion_labels", map[string]any{
				"id": s.id("discussion_label"), "ticket_id": s.recent("tickets", 2)[0].get("id"), "attempt": 1, "round": 1,
				"label": "yours", "assessed_at": s.at(0), "created_at": s.at(0),
			})
			return nil
		}),
		"steward_discussion_decisions": tenantSeeder(func(s *seed) error {
			s.insert("steward_discussion_decisions", map[string]any{
				"id": s.id("discussion_decision"), "label_id": s.id("discussion_label"), "action": "approve",
				"original_body": "draft", "final_body": "draft", "actor": "owner", "created_at": s.at(0),
			})
			return nil
		}),
		"steward_discussion_applies": tenantSeederAfter(func(s *seed) error {
			s.insert("steward_discussion_applies", map[string]any{
				"label_id": s.id("discussion_label"), "tier": "veto", "apply_at": s.at(1), "created_at": s.at(0),
			})
			return nil
		}, "steward_discussion_labels"),
	})
}
