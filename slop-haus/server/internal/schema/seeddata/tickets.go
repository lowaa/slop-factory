package seeddata

import (
	"fmt"
	"time"
)

func init() {
	register(Registry{

		"agents": tenantSeederAfter(func(s *seed) error {
			// The owner must be a member of the tenant, which a trigger checks:
			// hence the ordering after the memberships'. First, so the two most
			// recent agents the other seeders pick are still the workers.
			s.insert("agents", map[string]any{
				"id": s.id("conductor"), "name": s.tag("conductor"), "kind": "conductor",
				"owner_user_id": s.lastFor(s.tenant, "global_users").get("id"),
			})
			for i := 0; i < 2; i++ {
				agent := map[string]any{
					"id": s.id(fmt.Sprintf("agent%d", i)), "name": s.tag(fmt.Sprintf("agent%d", i)),
				}
				if i == 1 {
					agent["kind"] = "console"
				}
				s.insert("agents", agent)
			}
			return nil
		}, "global_tenant_memberships"),
		"tickets": tenantSeeder(func(s *seed) error {
			agent, user := s.recent("agents", 2)[0].get("id"), s.lastFor(s.tenant, "global_users").get("id")
			statuses := []string{"open", "in_progress", "blocked", "discussion", "done", "cancelled", "preparing", "review", "parked", "in_merge_queue"}
			for i, status := range statuses {
				values := map[string]any{
					"title": s.tag("ticket-" + status), "body": "body " + status, "status": status,
					"priority": []string{"low", "normal", "high", "urgent"}[i%4], "created_at": s.at(time.Duration(i) * time.Minute),
					"updated_at": s.at(time.Duration(i)*time.Minute + time.Hour), "revision": i,
				}
				if i%2 == 0 {
					values["created_by_agent_id"] = agent
				} else {
					values["created_by_user_id"] = user
				}
				switch status {
				case "discussion":
					values["needs_discussion"] = true
					values["resume_agent_id"], values["discussion_agent_id"] = agent, agent
					values["affinity_until"] = s.at(2 * time.Hour)
				case "review":
					values["requires_approval"] = true
					values["pr_url"] = "https://github.example/pull/" + s.tag("review")
					values["review_started_at"] = s.at(3 * time.Hour)
					values["role_match"] = "all"
				case "done":
					values["closed_at"] = s.at(4 * time.Hour)
					values["requires_approval"] = true
					values["pr_url"] = "https://github.example/pull/" + s.tag("done")
					values["approved_at"], values["approved_by_user_id"] = s.at(3*time.Hour), user
					values["post_reviewed_at"], values["post_reviewed_by_user_id"] = s.at(4*time.Hour), user
				case "in_progress":
					values["changes_requested_at"] = s.at(3 * time.Hour)
					values["requires_approval"] = true
				}
				ticket := s.insert("tickets", values)
				s.moveToFirstAttempt(ticket, values)
				if status == "done" {
					s.promoteFirstAttempt(ticket, user)
				}
				if status == "discussion" {
					s.recordFirstAttemptSession(ticket)
				}
			}
			return nil
		}),
		"ticket_paths": tenantSeeder(func(s *seed) error {
			ticket := s.recent("tickets", 2)[0].get("id")
			s.insert("ticket_paths", map[string]any{"ticket_id": ticket, "path": s.tag("a/"), "source": "filed"})
			s.insert("ticket_paths", map[string]any{"ticket_id": ticket, "path": s.tag("b/"), "source": "extended"})
			return nil
		}),
		"ticket_deps": tenantSeeder(func(s *seed) error {
			tickets := s.recent("tickets", 2)
			s.insert("ticket_deps", map[string]any{
				"ticket_id": tickets[1].get("id"), "depends_on_ticket_id": tickets[0].get("id"),
				"added_by_agent_id": s.recent("agents", 2)[0].get("id"), "created_at": s.at(0),
			})
			return nil
		}),
		"ticket_comments": tenantSeeder(func(s *seed) error {
			ticket := s.recent("tickets", 2)[0].get("id")
			s.insert("ticket_comments", map[string]any{
				"ticket_id": ticket, "author_agent_id": s.recent("agents", 2)[0].get("id"), "body": s.tag("agent comment"),
			})
			s.insert("ticket_comments", map[string]any{
				"ticket_id": ticket, "author_user_id": s.lastFor(s.tenant, "global_users").get("id"), "body": s.tag("human comment"),
			})
			s.insert("ticket_comments", map[string]any{
				"ticket_id": ticket, "author_agent_id": s.recent("agents", 2)[0].get("id"), "body": s.tag("comment on someone's behalf"),
				"on_behalf_of_user_id": s.lastFor(s.tenant, "global_users").get("id"),
			})
			return nil
		}),
		"ticket_revisions": tenantSeeder(func(s *seed) error {
			ticket := s.recent("tickets", 2)[0].get("id")
			s.insert("ticket_revisions", map[string]any{
				"ticket_id": ticket, "revision": 1, "editor_agent_id": s.recent("agents", 2)[0].get("id"),
				"old_title": "old", "new_title": s.tag("new"), "old_body": "old body", "new_body": s.tag("new body"),
			})
			s.insert("ticket_revisions", map[string]any{
				"ticket_id": ticket, "revision": 2, "editor_user_id": s.lastFor(s.tenant, "global_users").get("id"),
				"old_title": s.tag("new"), "new_title": s.tag("newer"),
			})
			return nil
		}),
		"ticket_snapshots": tenantSeederAfter(func(s *seed) error {
			s.insert("ticket_snapshots", map[string]any{
				"ticket_id": s.recent("tickets", 2)[0].get("id"), "revision": 1, "title": s.tag("title"), "body": s.tag("body"),
				"paths": `[{"path": "a/", "source": "filed"}]`, "comments_through_id": 1, "as_of": s.at(0), "source": "refrozen",
				"taken_by_user_id": s.lastFor(s.tenant, "global_users").get("id"),
			})
			return nil
		}, "global_users"),
		"attempt_verdicts": tenantSeederAfter(func(s *seed) error {
			s.insert("attempt_verdicts", map[string]any{
				"ticket_id": s.recent("tickets", 2)[0].get("id"), "attempt_number": 1, "verdict": "would_merge_with_changes",
				"note": s.tag("note"), "set_by_user_id": s.lastFor(s.tenant, "global_users").get("id"), "set_at": s.at(0),
			})
			return nil
		}, "global_users"),
		"agent_contexts": tenantSeeder(func(s *seed) error {
			s.insert("agent_contexts", map[string]any{
				"ticket_id": s.recent("tickets", 2)[0].get("id"), "attempt_number": 1, "agent_id": s.recent("agents", 2)[0].get("id"),
				"body": s.tag("context"),
			})
			return nil
		}),
		"sessions": tenantSeeder(func(s *seed) error {
			agents := s.recent("agents", 2)
			s.insert("sessions", map[string]any{
				"agent_id": agents[0].get("id"), "claude_session_id": s.tag("session"), "cwd": "/work/" + s.tag("cwd"),
				"started_at": s.at(0), "last_seen_at": s.at(time.Hour), "last_progress_at": s.at(30 * time.Minute),
				"last_block_at": s.at(40 * time.Minute), "stop_block_count": 2, "harness": "claude", "harness_version": "1.2.3",
				"provider": "anthropic", "model": "m", "labels": `{"k": "v"}`, "config_revision": 3,
				"stuck_reason": "no progress", "stuck_at": s.at(50 * time.Minute),
			})
			s.insert("sessions", map[string]any{
				"agent_id": agents[1].get("id"), "claude_session_id": s.tag("ended-session"), "ended_at": s.at(2 * time.Hour),
				"started_at": s.at(0), "last_seen_at": s.at(time.Hour),
			})
			return nil
		}),
		"agent_roles": tenantSeeder(func(s *seed) error {
			s.insert("agent_roles", map[string]any{"id": s.id("role"), "name": s.tag("role"), "description": "does things"})
			return nil
		}),
		"agent_role_members": tenantSeeder(func(s *seed) error {
			s.insert("agent_role_members", map[string]any{
				"agent_id": s.recent("agents", 2)[0].get("id"), "role_id": s.last("agent_roles").get("id"),
			})
			return nil
		}),
		"ticket_required_roles": tenantSeeder(func(s *seed) error {
			s.insert("ticket_required_roles", map[string]any{
				"ticket_id": s.recent("tickets", 2)[0].get("id"), "role_id": s.last("agent_roles").get("id"),
			})
			return nil
		}),
		"agent_messages": tenantSeeder(func(s *seed) error {
			agent, user := s.recent("agents", 2)[0].get("id"), s.lastFor(s.tenant, "global_users").get("id")
			s.insert("agent_messages", map[string]any{
				"agent_id": agent, "kind": "message", "author_user_id": user, "body": s.tag("direct"), "created_at": s.at(0),
			})
			s.insert("agent_messages", map[string]any{
				"agent_id": agent, "kind": "ticket_comment", "author_user_id": user, "ticket_id": s.recent("tickets", 2)[0].get("id"), "attempt_number": 1,
				"body": s.tag("comment copy"), "created_at": s.at(time.Hour), "delivered_at": s.at(2 * time.Hour),
			})
			return nil
		}),
	})
}
