package seeddata

import "time"

func init() {
	register(Registry{
		"global_oauth_clients": globalSeeder(func(s *seed) error {
			s.insert("global_oauth_clients", map[string]any{
				"id": s.tag("client"), "name": "client", "redirect_uris": []string{"https://client.example/cb"},
				"grant_types": []string{"authorization_code", "refresh_token"}, "secret_hash": s.tag("secret"),
				"last_used_at": s.at(time.Hour),
			})
			return nil
		}),
		"global_oauth_grants": globalSeeder(func(s *seed) error {
			for i, tenant := range s.w.tenants {
				ts := s.as(tenant)
				scope := "agent"
				if i == 1 {
					scope = "read"
				}
				ts.insert("global_oauth_grants", map[string]any{
					"id": ts.id("grant"), "tenant": tenant, "agent_id": ts.recent("agents", 2)[0].get("id"),
					"client_id": s.last("global_oauth_clients").get("id"), "approved_by": ts.last("global_users").get("id"),
					"resource": "https://slop-haus.example/mcp", "scope": scope, "last_used_at": ts.at(time.Hour),
				})
			}
			return nil
		}),
		"global_oauth_auth_requests": globalSeeder(func(s *seed) error {
			s.insert("global_oauth_auth_requests", map[string]any{
				"id": s.id("authreq"), "client_id": s.last("global_oauth_clients").get("id"),
				"redirect_uri": "https://client.example/cb", "code_challenge": s.tag("challenge"), "state": "st",
				"resource": "https://slop-haus.example/mcp", "expires_at": s.at(time.Hour),
			})
			return nil
		}),
		"global_oauth_codes": globalSeeder(func(s *seed) error {
			for _, tenant := range s.w.tenants {
				ts := s.as(tenant)
				ts.insert("global_oauth_codes", map[string]any{
					"code_hash": ts.tag("code-" + tenant.String()), "grant_id": ts.last("global_oauth_grants").get("id"),
					"redirect_uri": "https://client.example/cb", "code_challenge": ts.tag("challenge"), "expires_at": ts.at(time.Hour),
				})
			}
			return nil
		}),
		"global_oauth_device_codes": globalSeeder(func(s *seed) error {
			s.insert("global_oauth_device_codes", map[string]any{
				"id": s.id("device"), "device_code_hash": s.tag("device-hash"), "user_code": s.tag("USER"),
				"client_id": s.last("global_oauth_clients").get("id"), "resource": "https://slop-haus.example/mcp",
				"expires_at": s.at(time.Hour), "grant_id": s.lastFor(s.w.tenants[0], "global_oauth_grants").get("id"),
				"started_from": "10.1.2.3", "created_at": s.at(0),
			})
			return nil
		}),
		"global_oauth_tokens": globalSeeder(func(s *seed) error {
			for _, tenant := range s.w.tenants {
				ts := s.as(tenant)
				ts.insert("global_oauth_tokens", map[string]any{
					"token_hash": ts.tag("token-" + tenant.String()), "grant_id": ts.last("global_oauth_grants").get("id"),
					"kind": "access", "expires_at": ts.at(time.Hour),
				})
			}
			return nil
		}),
	})
}
