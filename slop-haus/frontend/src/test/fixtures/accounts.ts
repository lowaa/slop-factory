import type {
  AuthConfig,
  Grant,
  Invite,
  InvitePreview,
  Member,
  PendingAuthorization,
  PendingDeviceAuthorization,
  Profile,
  Tenant,
} from "@/api";

export function makeProfile(overrides: Partial<Profile> = {}): Profile {
  return {
    id: "33333333-3333-3333-3333-333333333333",
    email: "person@example.com",
    display_name: "Ada Lovelace",
    tenant_id: "44444444-4444-4444-4444-444444444444",
    is_admin: false,
    ...overrides,
  };
}

export function makeInvite(overrides: Partial<Invite> = {}): Invite {
  return {
    id: "55555555-5555-5555-5555-555555555555",
    email: "invitee@example.com",
    kind: "join_tenant",
    tenant_id: "44444444-4444-4444-4444-444444444444",
    invited_by: "33333333-3333-3333-3333-333333333333",
    expires_at: "2026-01-08T00:00:00Z",
    created_at: "2026-01-01T00:00:00Z",
    ...overrides,
  };
}

export function makeInvitePreview(overrides: Partial<InvitePreview> = {}): InvitePreview {
  return {
    email: "invitee@example.com",
    kind: "join_tenant",
    tenant_name: "Ada's workspace",
    invited_by: "Ada Lovelace",
    account_exists: false,
    ...overrides,
  };
}

export function makeTenant(overrides: Partial<Tenant> = {}): Tenant {
  return {
    id: "44444444-4444-4444-4444-444444444444",
    name: "Ada's workspace",
    current: true,
    colour: null,
    ...overrides,
  };
}

export function makeMember(overrides: Partial<Member> = {}): Member {
  return {
    id: "33333333-3333-3333-3333-333333333333",
    display_name: "Ada Lovelace",
    ...overrides,
  };
}

export function makePendingAuthorization(overrides: Partial<PendingAuthorization> = {}): PendingAuthorization {
  return { client_name: "Claude Code", redirect_host: "127.0.0.1:9876", ...overrides };
}

export function makePendingDeviceAuthorization(
  overrides: Partial<PendingDeviceAuthorization> = {},
): PendingDeviceAuthorization {
  return {
    client_name: "slop-haus workspace",
    user_code: "BCDF-GHJK",
    expires_at: "2026-01-01T00:10:00Z",
    started_from: "100.77.108.4",
    started_at: "2026-01-01T00:05:00Z",
    from_this_network: true,
    ...overrides,
  };
}

export function makeGrant(overrides: Partial<Grant> = {}): Grant {
  return {
    id: "66666666-6666-6666-6666-666666666666",
    agent_id: "11111111-1111-1111-1111-111111111111",
    agent_name: "worker-1",
    agent_kind: "worker",
    client_id: "c-claude-code",
    client_name: "Claude Code",
    created_at: "2026-01-01T00:00:00Z",
    last_used_at: null,
    ...overrides,
  };
}

export function makeAuthConfig(overrides: Partial<AuthConfig> = {}): AuthConfig {
  return { registration_open: true, password_login: true, providers: [], ...overrides };
}
