package seeddata

import (
	"fmt"
	"time"
)

func init() {
	register(Registry{
		// One run filed under an attempt and one unattributed, which differ in
		// every column, so a backfill that drops the attribution or copies
		// the wrong column shows.
		"review_runs": tenantSeeder(func(s *seed) error {
			agent, ticket := s.recent("agents", 2)[0].get("id"), s.recent("tickets", 2)[0].get("id")
			for i, label := range []string{"attributed", "loose"} {
				values := map[string]any{
					"id": s.id("review-run-" + label), "posted_by_kind": "agent", "posted_by_id": agent, "kind": "review", "status": "completed",
					"branch": s.tag("branch-" + label), "head_sha": fmt.Sprintf("%040d", i+1),
					"personas": []string{"standard", s.tag("persona")}, "verdict": "approve_with_comments", "incomplete": i == 1,
					"provider": "claude", "model": fmt.Sprintf("m%d", i),
					"started_at": s.at(time.Duration(i) * time.Hour), "finished_at": s.at(time.Duration(i)*time.Hour + 10*time.Minute),
					"received_at": s.at(time.Duration(i)*time.Hour + 11*time.Minute),
					"report":      map[string]any{"summary": s.tag("summary-" + label)},
				}
				if i == 0 {
					values["ticket_id"], values["attempt_number"] = ticket, 1
				} else {
					values["kind"], values["status"], values["verdict"] = "bench", "failed", ""
					values["posted_by_kind"] = "supervisor"
				}
				s.insert("review_runs", values)
			}
			return nil
		}),
		"review_findings": tenantSeeder(func(s *seed) error {
			for i, run := range s.recent("review_runs", 2) {
				s.insert("review_findings", map[string]any{
					"id": s.id(fmt.Sprintf("review-finding-%d", i)), "run_id": run.get("id"), "position": i,
					"file": s.tag("file"), "line": 10 + i, "severity": []string{"major", "nit"}[i],
					"summary": s.tag("finding"), "detail": s.tag("detail"), "personas": []string{"standard"},
				})
			}
			return nil
		}),
		"review_finding_labels": tenantSeeder(func(s *seed) error {
			agent := s.recent("agents", 2)[0].get("id")
			for i, finding := range s.recent("review_findings", 2) {
				values := map[string]any{
					"finding_id": finding.get("id"), "agent_id": agent, "verdict": "false_positive",
					"reason": s.tag("reason"), "labelled_at": s.at(time.Duration(i) * time.Minute),
				}
				if i == 0 {
					values["verdict"], values["fixed_in"] = "confirmed", s.tag("sha")
				}
				s.insert("review_finding_labels", values)
			}
			return nil
		}),
		"review_requests": tenantSeeder(func(s *seed) error {
			agent, ticket := s.recent("agents", 2)[0].get("id"), s.recent("tickets", 2)[0].get("id")
			s.insert("review_requests", map[string]any{
				"ticket_id": ticket, "attempt_number": 1, "agent_id": agent, "head_sha": fmt.Sprintf("%040x", 0xabc),
				"intent": s.tag("intent"), "requested_at": s.at(30 * time.Minute),
			})
			return nil
		}),
	})
}
