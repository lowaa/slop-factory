package seeddata

import "time"

func init() {
	register(Registry{
		// Kept by a trigger on codehost_credentials, whose seeded row has no
		// webhook secret: a row is put here directly so the table has data.
		"global_github_app_installations": globalSeeder(func(s *seed) error {
			for _, tenant := range s.w.tenants {
				ts := s.as(tenant)
				ts.upsert("global_github_app_installations", "(tenant)", map[string]any{
					"tenant": tenant, "installation_id": 2, "credential_id": tenant.String(), "webhook_secret_enc": []byte("not a real blob"),
				})
			}
			return nil
		}),
		"github_deliveries": tenantSeeder(func(s *seed) error {
			values := map[string]any{
				"delivery_id": s.tag("delivery"), "installation_id": 2, "event": "pull_request",
				"action": "closed", "outcome": "applied", "detail": "merged", "ticket_id": s.recent("tickets", 2)[0].get("id"), "attempt_number": 1,
				"received_at": s.at(time.Hour),
			}
			// Until the endpoint column was dropped, a delivery names the endpoint it came through.
			if s.cols["global_github_webhooks"] != nil {
				values["endpoint_id"] = s.last("global_github_webhooks").get("id")
			}
			s.insert("github_deliveries", values)
			return nil
		}),
		"github_event_keys": tenantSeeder(func(s *seed) error {
			s.insert("github_event_keys", map[string]any{
				"event_key": "review:" + s.tag("review"), "delivery_id": s.tag("delivery"), "state": "applied", "claimed_at": s.at(time.Hour),
			})
			return nil
		}),
	})
}
