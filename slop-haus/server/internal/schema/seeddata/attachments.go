package seeddata

import "time"

func init() {
	register(Registry{
		// A key about to be written, as an upload that died before its row
		// leaves it. Not the key of the attachment below: that insert clears
		// its own reservation.
		"attachment_reservations": tenantSeeder(func(s *seed) error {
			s.insert("attachment_reservations", map[string]any{
				"storage_key": s.tag("reserved") + ".png", "size_bytes": 100, "reserved_at": s.at(0),
			})
			return nil
		}),
		"attachment_delete_queue": tenantSeeder(func(s *seed) error {
			// Already swept, and in the future so it is not pruned: a running api's
			// sweeper takes a pending row at its next pass, which an upgrade check
			// reads as the migration changing it.
			s.insert("attachment_delete_queue", map[string]any{
				"storage_key": s.tag("queued") + ".png", "reason": "deleted", "queued_at": s.at(0), "swept_at": s.at(0),
			})
			return nil
		}),
		"attachments": tenantSeeder(func(s *seed) error {
			// Reserved first, since the database refuses a row without one. Not
			// through s.insert: the row's insert deletes it, and a seeded row
			// that is gone would read as lost.
			key := s.tag("attached") + ".png"
			if _, err := s.q.Exec(s.ctx, `INSERT INTO attachment_reservations (storage_key, size_bytes) VALUES ($1, 100)`, key); err != nil {
				return err
			}
			s.insert("attachments", map[string]any{
				"id": s.id("attachment"), "ticket_id": s.recent("tickets", 1)[0].get("id"),
				"uploader_kind": "user", "uploader_user_id": s.lastFor(s.tenant, "global_users").get("id"),
				"filename": "shot.png", "content_type": "image/png", "size_bytes": 100,
				"sha256": make([]byte, 32), "storage_key": key, "created_at": s.at(time.Hour),
			})
			return nil
		}),
		"attachment_usage": derivedSeeder("attachments"),
	})
}
