package seeddata

import "time"

// Kept apart from the registry literal so adding it does not touch the file
// every table's seeder is edited in.
func init() {
	Seeders["global_former_memberships"] = globalSeeder(func(s *seed) error {
		for i, tenant := range s.w.tenants {
			ts := s.as(tenant)
			// Someone from another tenant, so the row names a person who is
			// not also a current member of this one.
			other := s.w.tenants[(i+1)%len(s.w.tenants)]
			ts.insert("global_former_memberships", map[string]any{
				"tenant": tenant, "user_id": s.lastFor(other, "global_users").get("id"), "left_at": ts.at(time.Hour),
			})
		}
		return nil
	})
}
