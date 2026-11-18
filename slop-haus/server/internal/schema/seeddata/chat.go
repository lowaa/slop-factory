package seeddata

import "time"

func init() {
	register(Registry{
		// A conductor's live conversation, and an ended one it had before, each
		// with transcript events and the host's queue: one item delivered and
		// acknowledged, one waiting. The conductor is the first of the round's
		// agents, hence three back.
		"conductor_conversations": tenantSeeder(func(s *seed) error {
			conductor, user := s.recent("agents", 3)[0].get("id"), s.lastFor(s.tenant, "global_users").get("id")
			draft := map[string]any{
				"id": s.id("draft-conversation"), "conductor_id": conductor, "owner_user_id": user,
				"state": "stopped", "next_seq": 2,
				"host_acked": 1, "transcript_bytes": 5, "created_at": s.at(0), "updated_at": s.at(time.Hour),
			}
			ticket := map[string]any{
				"id": s.id("ticket-conversation"), "conductor_id": conductor, "owner_user_id": user,
				"state": "ready", "next_seq": 3, "host_acked": 2, "transcript_bytes": 11,
				"created_at": s.at(2 * time.Hour), "updated_at": s.at(3 * time.Hour),
			}
			s.insert("conductor_conversations", draft)
			s.insert("conductor_conversations", ticket)
			return nil
		}),
		"conductor_conversation_events": tenantSeeder(func(s *seed) error {
			drafting, ticketing := s.recent("conductor_conversations", 2)[0].get("id"), s.recent("conductor_conversations", 2)[1].get("id")
			for i, e := range []struct {
				conversation any
				seq          int
				kind, body   string
			}{
				{drafting, 1, "user", "hello"}, {drafting, 2, "stopped", "stopped by its owner"},
				{ticketing, 1, "user", "what is this?"}, {ticketing, 2, "ready", "0123abc"}, {ticketing, 3, "assistant", "a ticket"},
			} {
				s.insert("conductor_conversation_events", map[string]any{
					"conversation_id": e.conversation, "seq": e.seq, "kind": e.kind, "body": e.body,
					"created_at": s.at(time.Duration(i) * time.Minute),
				})
			}
			return nil
		}),
		"conductor_queues": tenantSeeder(func(s *seed) error {
			s.insert("conductor_queues", map[string]any{
				"conductor_id": s.recent("agents", 3)[0].get("id"), "next_seq": 3, "acked_seq": 1,
				"host_id": s.id("host"), "host_seen_at": s.at(time.Hour),
			})
			return nil
		}),
		"conductor_work": tenantSeeder(func(s *seed) error {
			conductor, conversation := s.recent("agents", 3)[0].get("id"), s.recent("conductor_conversations", 2)[1].get("id")
			s.insert("conductor_work", map[string]any{
				"conductor_id": conductor, "seq": 2, "kind": "message", "conversation_id": conversation, "message_seq": 1,
				"created_at": s.at(time.Hour),
			})
			last := map[string]any{
				"conductor_id": conductor, "seq": 3, "kind": "stop", "conversation_id": conversation,
				"created_at": s.at(2 * time.Hour),
			}
			s.insert("conductor_work", last)
			return nil
		}),
		"pending_actions": tenantSeeder(func(s *seed) error {
			agent, ticket := s.recent("agents", 2)[0].get("id"), s.recent("tickets", 2)[0].get("id")
			s.insert("pending_actions", map[string]any{
				"id": s.id("pending"), "ticket_id": ticket, "agent_id": agent, "kind": "status", "payload": `{"to": "done"}`,
				"base_status": "open", "base_revision": 1, "base_pr_url": "https://github.example/pull/1",
				"base_review_started_at": s.at(0), "created_at": s.at(0), "expires_at": s.at(24 * time.Hour),
			})
			s.insert("pending_actions", map[string]any{
				"id": s.id("decided"), "ticket_id": ticket, "agent_id": agent, "kind": "ready", "payload": `{}`,
				"base_status": "preparing", "base_revision": 0, "status": "approved", "outcome": "ok", "created_at": s.at(0),
				"expires_at": s.at(24 * time.Hour), "decided_at": s.at(time.Hour),
				"decided_by_user_id": s.lastFor(s.tenant, "global_users").get("id"),
			})
			// The kinds that are about an attempt, which exist once tickets have attempts.
			for _, kind := range []string{"discussion_answer", "review_decision"} {
				s.insert("pending_actions", map[string]any{
					"id": s.id(kind), "ticket_id": ticket, "attempt_number": 1, "agent_id": agent, "kind": kind, "payload": `{}`,
					"base_status": "discussion", "base_revision": 1, "created_at": s.at(0), "expires_at": s.at(24 * time.Hour),
				})
			}
			return nil
		}),
	})
}
