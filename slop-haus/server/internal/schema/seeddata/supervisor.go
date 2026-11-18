package seeddata

import "time"

func init() {
	register(Registry{
		"supervisor_slot_states": tenantSeeder(func(s *seed) error {
			s.insert("supervisor_slot_states", map[string]any{
				"agent_id": s.recent("agents", 1)[0].get("id"), "phase": "backoff", "phase_entered_at": s.at(0),
				"error_class": s.tag("error_class"), "error_streak": 2, "reported_at": s.at(time.Minute),
			})
			return nil
		}),
		"remote_control_host_leases": tenantSeeder(func(s *seed) error {
			s.insert("remote_control_host_leases", map[string]any{
				"agent_id": s.recent("agents", 1)[0].get("id"), "holder": s.tag("holder"), "expires_at": s.at(time.Hour),
			})
			return nil
		}),
		"agent_workspace_layouts": tenantSeeder(func(s *seed) error {
			s.insert("agent_workspace_layouts", map[string]any{
				"agent_id": s.recent("agents", 2)[0].get("id"), "layout": `{"repos": ["a"]}`, "reported_at": s.at(0),
			})
			return nil
		}),
		"supervisor_desired_state": tenantSeeder(func(s *seed) error {
			s.insert("supervisor_desired_state", map[string]any{"supervisor_id": s.recent("agents", 1)[0].get("id"), "revision": 2})
			return nil
		}),
		"supervisor_desired_agents": tenantSeederAfter(func(s *seed) error {
			s.insert("supervisor_desired_agents", map[string]any{
				"id": s.id("desired_agent"), "supervisor_id": s.recent("agents", 1)[0].get("id"), "kind": "slot", "name": s.tag("slot"),
				"harness": "claude", "state": "running", "generation": 2, "resume_generation": 1, "observed_generation": 1,
				"observed_phase": "running", "created_at": s.at(0),
			})
			return nil
		}, "supervisor_desired_state"),
		"workspace_manifests": tenantSeeder(func(s *seed) error {
			s.upsert("workspace_manifests", "(tenant_id)", map[string]any{
				"tenant_id": s.tenant, "repos": `[{"url": "https://git.example/r"}]`, "updated_at": s.at(0),
				"updated_by": s.lastFor(s.tenant, "global_users").get("id"),
			})
			return nil
		}),
		"workspace_manifest_revisions": tenantSeeder(func(s *seed) error {
			s.upsert("workspace_manifest_revisions", "(tenant_id)", map[string]any{"tenant_id": s.tenant, "revision": 3})
			return nil
		}),
	})
}
