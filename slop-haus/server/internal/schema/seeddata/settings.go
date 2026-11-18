package seeddata

import (
	"time"

	"github.com/google/uuid"
)

func init() {
	register(Registry{
		"tenant_settings": tenantSeeder(func(s *seed) error {
			user := s.last("global_users").get("id")
			for _, v := range []bool{true, false} {
				s.insert("tenant_settings", map[string]any{
					"setting_name": s.tag("ts-bool"), "setting_type": "bool", "bool_value": v, "changed_by": user,
				})
			}
			s.insert("tenant_settings", map[string]any{"setting_name": s.tag("ts-str"), "setting_type": "str", "str_value": "v"})
			s.insert("tenant_settings", map[string]any{"setting_name": s.tag("ts-json"), "setting_type": "jsonb", "jsonb_value": `{"k":1}`})
			return nil
		}),
		"harness_config": tenantSeeder(func(s *seed) error {
			user := s.lastFor(s.tenant, "global_users").get("id")
			s.upsert("harness_config", "(tenant_id, agent_id)", map[string]any{
				"tenant_id": s.tenant, "agent_id": nil, "settings": `{"model": "default"}`, "revision": s.round + 1,
				"updated_by": user, "updated_at": s.at(0),
			})
			s.insert("harness_config", map[string]any{
				"agent_id": s.recent("agents", 2)[0].get("id"), "settings": `{"model": "special"}`, "revision": s.round + 1,
				"updated_by": user, "updated_at": s.at(time.Hour),
			})
			return nil
		}),
		"harness_config_revisions": tenantSeeder(func(s *seed) error {
			s.upsert("harness_config_revisions", "(tenant_id)", map[string]any{"tenant_id": s.tenant, "revision": s.round + 1})
			return nil
		}),
		"fleet_instruction_versions": tenantSeeder(func(s *seed) error {
			user := s.lastFor(s.tenant, "global_users").get("id")
			s.insert("fleet_instruction_versions", map[string]any{
				"name": s.tag("house"), "version": 1, "body": "first", "created_by": user, "created_at": s.at(0),
			})
			s.insert("fleet_instruction_versions", map[string]any{
				"name": s.tag("house"), "version": 2, "body": "second", "created_by": user, "created_at": s.at(time.Hour),
			})
			return nil
		}),
		"fleet_instruction_defaults": tenantSeeder(func(s *seed) error {
			// Keyed by kind once the table gained its kind column; the seeded fleet row is the only one this adds.
			target := "(tenant_id)"
			if s.cols["fleet_instruction_defaults"]["kind"] {
				target = "(tenant_id, kind)"
			}
			s.upsert("fleet_instruction_defaults", target, map[string]any{
				"kind": "fleet", "tenant_id": s.tenant, "version_id": s.last("fleet_instruction_versions").get("id"), "updated_at": s.at(0),
				"updated_by": s.lastFor(s.tenant, "global_users").get("id"),
			})
			return nil
		}),
		"fleet_instruction_archives": tenantSeeder(func(s *seed) error {
			s.insert("fleet_instruction_archives", map[string]any{
				"version_id": s.recent("fleet_instruction_versions", 2)[0].get("id"), "archived_at": s.at(0),
				"archived_by": s.lastFor(s.tenant, "global_users").get("id"),
			})
			return nil
		}),
		"variant_versions": tenantSeeder(func(s *seed) error {
			user := s.lastFor(s.tenant, "global_users").get("id")
			s.insert("variant_versions", map[string]any{
				"name": s.tag("fast"), "version": 1, "harness": "claude", "settings": `{"model":"sonnet"}`,
				"fleet_instructions_version_id": s.recent("fleet_instruction_versions", 2)[0].get("id"),
				"created_by":                    user, "created_at": s.at(0),
			})
			s.insert("variant_versions", map[string]any{
				"name": s.tag("fast"), "version": 2, "harness": "claude", "settings": `{"model":"sonnet"}`,
				"created_by": user, "created_at": s.at(time.Hour),
			})
			return nil
		}),
		"eval_sets": tenantSeeder(func(s *seed) error {
			user := s.lastFor(s.tenant, "global_users").get("id")
			s.insert("eval_sets", map[string]any{
				"name": s.tag("pinned"), "description": "benchmark", "ticket_ids": []int64{1, 2},
				"created_by": user, "created_at": s.at(0), "updated_at": s.at(0),
			})
			s.insert("eval_sets", map[string]any{
				"name": s.tag("filtered"), "filter": `{"q":"parser"}`,
				"created_by": user, "created_at": s.at(0), "updated_at": s.at(0),
			})
			return nil
		}),
		"eval_set_batches": tenantSeeder(func(s *seed) error {
			s.insert("eval_set_batches", map[string]any{
				"eval_set_id": s.recent("eval_sets", 2)[0].get("id"), "batch_id": uuid.New(),
				"version_ids": []any{s.recent("variant_versions", 2)[0].get("id"), nil}, "runs": 1, "ticket_count": 2,
				"created_by": s.lastFor(s.tenant, "global_users").get("id"), "created_at": s.at(0),
			})
			return nil
		}),
		"variant_archives": tenantSeeder(func(s *seed) error {
			s.insert("variant_archives", map[string]any{
				"version_id": s.recent("variant_versions", 2)[0].get("id"), "archived_at": s.at(0),
				"archived_by": s.lastFor(s.tenant, "global_users").get("id"),
			})
			return nil
		}),
	})
}
