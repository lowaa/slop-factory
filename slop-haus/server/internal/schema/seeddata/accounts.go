package seeddata

import (
	"fmt"
	"time"
)

func init() {
	register(Registry{
		"global_users": globalSeeder(func(s *seed) error {
			for i, tenant := range s.w.tenants {
				ts := s.as(tenant)
				var password any = "hash"
				if i%2 == 1 {
					password = nil // sign-in through an identity provider only
				}
				ts.insert("global_users", map[string]any{
					"id": ts.id(fmt.Sprintf("user%d", i)), "email": ts.tag(fmt.Sprintf("user%d@example.invalid", i)),
					"password_hash": password, "display_name": ts.tag("name"), "is_active": true,
					"is_admin": i == 0, "date_joined": ts.at(0), "sessions_valid_after": ts.at(time.Hour),
				})
			}
			return nil
		}),
		"global_tenants": globalSeeder(func(s *seed) error {
			for _, tenant := range s.w.tenants {
				ts := s.as(tenant)
				ts.upsert("global_tenants", "(id)", map[string]any{"id": tenant, "name": ts.tag("tenant")})
			}
			return nil
		}),
		"global_tenant_memberships": globalSeeder(func(s *seed) error {
			for i, tenant := range s.w.tenants {
				ts := s.as(tenant)
				role := "member"
				if i == 0 {
					role = "owner"
				}
				ts.insert("global_tenant_memberships", map[string]any{
					"id": ts.id("membership"), "tenant": tenant, "user_id": ts.last("global_users").get("id"), "role": role,
				})
			}
			return nil
		}),
		"global_auth_tokens": globalSeeder(func(s *seed) error {
			for _, tenant := range s.w.tenants {
				ts := s.as(tenant)
				ts.insert("global_auth_tokens", map[string]any{
					"id": ts.id("authtoken"), "user_id": ts.last("global_users").get("id"),
					"token_hash": ts.tag("authtoken-hash"), "purpose": "activation", "expires_at": ts.at(48 * time.Hour),
				})
			}
			return nil
		}),
		// The migration seeds claude and pi, which rows elsewhere name; a round
		// finds them already there.
		"global_harnesses": globalSeeder(func(s *seed) error {
			for _, name := range []string{"claude", "pi"} {
				s.upsert("global_harnesses", "(name)", map[string]any{"name": name})
			}
			return nil
		}),
		"global_auth_providers": globalSeeder(func(s *seed) error {
			s.insert("global_auth_providers", map[string]any{"name": s.tag("provider"), "issuer": "https://" + s.tag("issuer") + ".example"})
			return nil
		}),
		"global_user_identities": globalSeeder(func(s *seed) error {
			for _, tenant := range s.w.tenants {
				ts := s.as(tenant)
				ts.insert("global_user_identities", map[string]any{
					"provider": s.last("global_auth_providers").get("name"), "subject": ts.tag("subject-" + tenant.String()),
					"user_id": ts.last("global_users").get("id"), "login": "login", "email": "id@example.invalid",
					"last_login_at": ts.at(time.Hour),
				})
			}
			return nil
		}),
		"global_invites": globalSeeder(func(s *seed) error {
			for _, tenant := range s.w.tenants {
				ts := s.as(tenant)
				ts.insert("global_invites", map[string]any{
					"id": ts.id("invite"), "token_hash": ts.tag("invite-" + tenant.String()), "email": ts.tag("invitee@example.invalid"),
					"kind": "join_tenant", "tenant": tenant, "invited_by": ts.last("global_users").get("id"),
					"expires_at": ts.at(72 * time.Hour),
				})
			}
			return nil
		}),
		"global_consent_events": globalSeeder(func(s *seed) error {
			for _, tenant := range s.w.tenants {
				ts := s.as(tenant)
				user := ts.last("global_users").get("id")
				ts.insert("global_consent_events", map[string]any{
					"user_id": user, "purpose": "analytics", "granted": true, "document_kind": "privacy",
					"document_version": "v1", "source": "banner", "decided_at": ts.at(0), "session_id": ts.tag("sess"),
				})
				ts.insert("global_consent_events", map[string]any{
					"user_id": user, "purpose": "analytics", "granted": false, "document_kind": "privacy",
					"document_version": "v1", "source": "settings", "decided_at": ts.at(time.Hour),
					"merged_from_anonymous": true, "merge_token": ts.tag("merge-" + tenant.String()),
				})
			}
			return nil
		}),
		"global_user_settings": globalSeeder(func(s *seed) error {
			for _, tenant := range s.w.tenants {
				ts := s.as(tenant)
				user := ts.last("global_users").get("id")
				ts.insert("global_user_settings", map[string]any{
					"user_id": user, "setting_name": "remote_control.session_switch_explained", "setting_type": "bool", "bool_value": true,
				})
				ts.insert("global_user_settings", map[string]any{
					"user_id": user, "setting_name": "remote_control.session_switch_explained", "setting_type": "bool", "bool_value": false,
				})
			}
			return nil
		}),
		"user_tenant_settings": tenantSeeder(func(s *seed) error {
			user := s.last("global_users").get("id")
			// The table's restrictive policy admits only the transaction's user.
			if _, err := s.q.Exec(s.ctx, `SELECT set_config('app.current_user_id', $1, true)`, fmt.Sprint(user)); err != nil {
				return err
			}
			for _, v := range []bool{true, false} {
				s.insert("user_tenant_settings", map[string]any{
					"user_id": user, "setting_name": "auto_approve", "setting_type": "bool", "bool_value": v,
				})
			}
			return nil
		}),
		"global_consent_merge_tokens": globalSeeder(func(s *seed) error {
			for _, tenant := range s.w.tenants {
				ts := s.as(tenant)
				ts.insert("global_consent_merge_tokens", map[string]any{"token_hash": ts.tag("merge-" + tenant.String())})
			}
			return nil
		}),
		"global_legal_document_versions": globalSeeder(func(s *seed) error {
			s.insert("global_legal_document_versions", map[string]any{
				"kind": "privacy", "version": s.tag("v"), "locale": "en", "effective": s.at(0), "material": true,
				"reviewed": true, "reviewed_by": "counsel", "reviewed_on": s.at(0), "body": "text " + s.tag("doc"),
				"content_sha256": s.tag("sha"),
			})
			return nil
		}),
		"global_intake_credentials": globalSeeder(func(s *seed) error {
			for _, tenant := range s.w.tenants {
				ts := s.as(tenant)
				ts.insert("global_intake_credentials", map[string]any{
					"source_id": ts.id("intake-source"), "tenant": tenant, "kind": "bearer",
					"token_hash": []byte(ts.tag("intake-token-hash")), "generation": 2,
				})
			}
			return nil
		}),
	})
}
