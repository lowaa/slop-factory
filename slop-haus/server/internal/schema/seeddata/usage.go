package seeddata

import (
	"fmt"
	"time"
)

func init() {
	register(Registry{
		"llm_usage": tenantSeeder(func(s *seed) error {
			agent, ticket := s.recent("agents", 2)[0].get("id"), s.recent("tickets", 2)[0].get("id")
			// Each transcript differs in model and in when it started and ended, so a
			// backfill that ignores a join column or copies the wrong timestamp shows.
			for i, transcript := range []string{"t1", "t2"} {
				values := map[string]any{
					"agent_id": agent, "transcript_key": s.tag(transcript), "harness": "h", "session_id": "s", "scope_hint": "main",
					"scope": "overhead", "provider": "p", "model": fmt.Sprintf("m%d", i), "billing_mode": "metered",
					"requests": 1 + i, "input_tokens": 10 + i, "output_tokens": 20 + i, "cache_read_tokens": 1, "cache_write_tokens": 2,
					"reasoning_tokens": 3, "reported_cost_microdollars": 100 + i, "price_version": "v", "unpriced": false,
					"started_at": s.at(time.Duration(i) * time.Hour), "ended_at": s.at(time.Duration(i)*time.Hour + 30*time.Minute),
				}
				if i == 1 {
					values["scope"], values["ticket_id"], values["attempt_number"] = "ticket", ticket, 1
					values["estimated_cost_microdollars"] = 90
				}
				s.insert("llm_usage", values)
			}
			return nil
		}),
		"llm_usage_daily": tenantSeederAfter(func(s *seed) error {
			for i, usage := range s.recent("llm_usage", 2) {
				s.insert("llm_usage_daily", map[string]any{
					"day": s.at(0).UTC().Truncate(24 * time.Hour), "agent_id": usage.get("agent_id"), "provider": usage.get("provider"),
					"model": usage.get("model"), "billing_mode": usage.get("billing_mode"), "records": 1,
					"requests": 1 + i, "input_tokens": 10 + i, "output_tokens": 20 + i, "cache_read_tokens": 1,
					"cache_write_tokens": 2, "reasoning_tokens": 3, "cost_microdollars": 100 + i,
				})
			}
			return nil
		}, "llm_usage"),
		"llm_tool_usage": tenantSeeder(func(s *seed) error {
			for i, usage := range s.recent("llm_usage", 2) {
				for j, tool := range []string{"bash", "read"} {
					s.insert("llm_tool_usage", map[string]any{
						"agent_id": usage.get("agent_id"), "transcript_key": usage.get("transcript_key"), "provider": usage.get("provider"),
						"model": usage.get("model"), "harness": usage.get("harness"), "billing_mode": usage.get("billing_mode"),
						"category": "c", "tool": tool, "command": fmt.Sprintf("cmd%d", j), "calls": 5 + i, "errors": j, "duration_ms": 100 + j,
						"result_bytes": 1000 + i, "ended_at": usage.get("ended_at"),
					})
				}
			}
			return nil
		}),
		"usage_declarations": tenantSeederAfter(func(s *seed) error {
			agent, ticket := s.recent("agents", 2)[0].get("id"), s.recent("tickets", 2)[0].get("id")
			s.insert("usage_declarations", map[string]any{"agent_id": agent, "ticket_id": ticket, "declared_at": s.at(0)})
			s.insert("usage_declarations", map[string]any{"agent_id": agent, "declared_at": s.at(time.Hour)})
			return nil
		}, "agents", "tickets"),
		"llm_usage_marks": tenantSeederAfter(func(s *seed) error {
			for i, usage := range s.recent("llm_usage", 2) {
				s.insert("llm_usage_marks", map[string]any{
					"agent_id": usage.get("agent_id"), "transcript_key": usage.get("transcript_key"), "provider": usage.get("provider"),
					"model": usage.get("model"), "harness": usage.get("harness"), "billing_mode": usage.get("billing_mode"),
					"requests": 1 + i, "input_tokens": 10 + i, "output_tokens": 20 + i, "cache_read_tokens": 1, "cache_write_tokens": 2,
					"reasoning_tokens": 3, "reported_cost_microdollars": 100 + i, "estimated_cost_microdollars": 90 + i,
				})
			}
			return nil
		}, "llm_usage"),
		"llm_guidance_usage": tenantSeederAfter(func(s *seed) error {
			ticket := s.recent("tickets", 2)[0].get("id")
			for i, usage := range s.recent("llm_usage", 2) {
				s.insert("llm_guidance_usage", map[string]any{
					"agent_id": usage.get("agent_id"), "transcript_key": usage.get("transcript_key"), "ticket_id": ticket,
					"provider": usage.get("provider"), "model": usage.get("model"), "harness": usage.get("harness"),
					"billing_mode": usage.get("billing_mode"), "requests": 1 + i, "input_tokens": 10 + i, "output_tokens": 20 + i,
					"cache_read_tokens": 1, "cache_write_tokens": 2, "reasoning_tokens": 3, "reported_cost_microdollars": 100 + i,
					"estimated_cost_microdollars": 90 + i, "price_version": "v", "unpriced": false, "ended_at": usage.get("ended_at"),
				})
			}
			return nil
		}, "llm_usage", "tickets"),
	})
}
