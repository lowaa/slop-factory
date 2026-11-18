package seeddata

import "time"

func init() {
	register(Registry{
		"leases": tenantSeeder(func(s *seed) error {
			agents, tickets := s.recent("agents", 2), s.recent("tickets", 2)
			sessions := s.recent("sessions", 2)
			s.insert("leases", map[string]any{
				"ticket_id": tickets[0].get("id"), "attempt_number": 1, "agent_id": agents[0].get("id"), "acquired_by_session_id": sessions[0].get("id"),
				"acquired_at": s.at(0), "expires_at": s.at(24 * time.Hour),
			})
			s.insert("leases", map[string]any{
				"ticket_id": tickets[1].get("id"), "attempt_number": 1, "agent_id": agents[1].get("id"), "acquired_at": s.at(0), "expires_at": s.at(24 * time.Hour),
			})
			// Held no more: a released lease, which the app keeps for a while.
			s.insert("leases", map[string]any{
				"ticket_id": tickets[1].get("id"), "attempt_number": 1, "agent_id": agents[0].get("id"), "acquired_at": s.at(0),
				"expires_at": s.at(time.Hour), "released_at": s.at(2 * time.Hour), "release_reason": "done",
				"revoked_to": "open", "revocation_delivered_at": s.at(2 * time.Hour),
			})
			return nil
		}),
		"scope_blocks": tenantSeeder(func(s *seed) error {
			tickets := s.recent("tickets", 2)
			s.insert("scope_blocks", map[string]any{
				"ticket_id": tickets[1].get("id"), "blocking_ticket_id": tickets[0].get("id"), "path": s.tag("a/"),
				"started_at": s.at(0), "ended_at": s.at(time.Hour), "end_reason": "claimed",
			})
			s.insert("scope_blocks", map[string]any{
				"ticket_id": tickets[1].get("id"), "blocking_ticket_id": tickets[0].get("id"), "path": s.tag("b/"), "started_at": s.at(2 * time.Hour),
			})
			return nil
		}),
		"scope_extension_denials": tenantSeeder(func(s *seed) error {
			tickets, agent := s.recent("tickets", 2), s.recent("agents", 2)[0].get("id")
			s.insert("scope_extension_denials", map[string]any{
				"ticket_id": tickets[1].get("id"), "agent_id": agent, "path": s.tag("a/x"), "blocking_ticket_id": tickets[0].get("id"),
				"blocking_path": s.tag("a/"), "denied_at": s.at(0), "resolved_at": s.at(time.Hour), "resolution": "granted",
			})
			s.insert("scope_extension_denials", map[string]any{
				"ticket_id": tickets[1].get("id"), "agent_id": agent, "path": s.tag("b/x"), "blocking_ticket_id": tickets[0].get("id"),
				"blocking_path": s.tag("b/"), "denied_at": s.at(2 * time.Hour),
			})
			return nil
		}),
		"scope_narrowings": tenantSeeder(func(s *seed) error {
			s.insert("scope_narrowings", map[string]any{
				"ticket_id": s.recent("tickets", 2)[0].get("id"), "agent_id": s.recent("agents", 2)[0].get("id"),
				"removed": 2, "kept": 1, "narrowed_at": s.at(0),
			})
			return nil
		}),
		"locks": tenantSeeder(func(s *seed) error {
			s.insert("locks", map[string]any{
				"name": s.tag("integration"), "holder_agent_id": s.recent("agents", 2)[0].get("id"),
				"holder_lease_id": s.recent("leases", 3)[0].get("id"), "acquired_at": s.at(0),
			})
			return nil
		}),
		"lock_holds": tenantSeeder(func(s *seed) error {
			s.insert("lock_holds", map[string]any{
				"name": s.tag("integration"), "ticket_id": s.recent("tickets", 2)[0].get("id"), "agent_id": s.recent("agents", 2)[0].get("id"),
				"queued_at": s.at(0), "acquired_at": s.at(time.Hour), "released_at": s.at(2 * time.Hour),
			})
			return nil
		}),
		"lock_waiters": tenantSeeder(func(s *seed) error {
			s.insert("lock_waiters", map[string]any{
				"name": s.tag("integration"), "agent_id": s.recent("agents", 2)[1].get("id"),
				"lease_id": s.recent("leases", 3)[1].get("id"), "queued_at": s.at(time.Hour),
			})
			return nil
		}),
		"worker_polls": tenantSeeder(func(s *seed) error {
			s.insert("worker_polls", map[string]any{
				"agent_id": s.recent("agents", 1)[0].get("id"), "last_polled_at": s.at(0), "version": s.tag("supervisor"),
			})
			return nil
		}),
		"lease_configs": tenantSeeder(func(s *seed) error {
			leases := s.recent("leases", 3)
			s.insert("lease_configs", map[string]any{
				"lease_id": leases[0].get("id"), "revision": 4, "settings": `{"claude":{"model":"sonnet"}}`,
				"variant_id":                    s.recent("variant_versions", 2)[0].get("id"),
				"fleet_instructions_version_id": s.recent("fleet_instruction_versions", 2)[0].get("id"),
				"harness":                       "claude", "harness_version": "2.1.0", "model": "sonnet", "bundle_hash": "abc123",
			})
			s.insert("lease_configs", map[string]any{"lease_id": leases[1].get("id"), "revision": 0, "settings": `{}`})
			return nil
		}),
	})
}
