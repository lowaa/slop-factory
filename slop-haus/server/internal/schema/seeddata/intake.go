package seeddata

import (
	"fmt"
	"time"
)

func init() {
	register(Registry{
		"intake_sources": tenantSeeder(func(s *seed) error {
			s.insert("intake_sources", map[string]any{
				"id": s.id("intake-source"), "name": s.tag("intake"), "adapter": "native", "trust": "untrusted",
				"auth_kind": "bearer", "default_status": "preparing", "default_priority": "normal", "max_priority": "high",
				"role_names": []string{"deploy"}, "role_match": "all", "paths": []string{"infra/"}, "reopen_policy": "reopen",
				"created_by": s.lastFor(s.tenant, "global_users").get("id"), "created_at": s.at(0), "last_delivery_at": s.at(time.Hour),
			})
			return nil
		}),
		"intake_events": tenantSeeder(func(s *seed) error {
			source := s.last("intake_sources").get("id")
			for i, ticket := range s.recent("tickets", 2) {
				s.insert("intake_events", map[string]any{
					"ticket_id": ticket.get("id"), "source_id": source, "delivery_id": s.tag(fmt.Sprintf("event-%d", i)),
					"event": "open", "title": "title", "body": "body", "link": "https://sender.example/1",
					"context": `{"k": "v"}`, "created_at": s.at(time.Duration(i) * time.Hour),
				})
			}
			return nil
		}),
		"intake_deliveries": tenantSeeder(func(s *seed) error {
			source := s.last("intake_sources").get("id")
			s.insert("intake_deliveries", map[string]any{
				"source_id": source, "delivery_id": s.tag("delivery"), "event": "open", "key": "k", "outcome": "applied",
				"downgraded": true, "detail": "created as preparing", "ticket_id": s.recent("tickets", 2)[0].get("id"),
				"received_at": s.at(time.Hour),
			})
			return nil
		}),
	})
}
