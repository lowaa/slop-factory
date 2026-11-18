import {
  BASE_URL,
  session,
  client,
  publicClient,
  credentialClient,
  probeClient,
  unwrap,
  raise,
  ApiError,
} from "./client";
import type {
  AuthConfig,
  Invite,
  InvitePreview,
  Member,
  Profile,
  SignInMethodsBody,
  Tenant,
  TenantOverview,
} from "./types";

export const sessionApi = {
  // -- Session --
  //
  // Sign-in sets the session as httpOnly cookies, so no token ever passes
  // through this code: a script that could read one could send it
  // anywhere.

  async register(
    email: string,
    password: string,
    displayName: string,
  ): Promise<void> {
    await unwrap(
      publicClient.POST("/api/auth/register", {
        body: { email, password, display_name: displayName },
      }),
    );
  },

  async activate(token: string): Promise<void> {
    await unwrap(publicClient.POST("/api/auth/activate", { body: { token } }));
  },

  async login(email: string, password: string): Promise<Profile> {
    return session.signedIn(
      await unwrap(
        publicClient.POST("/api/auth/login", { body: { email, password } }),
      ),
    );
  },

  async logout(): Promise<void> {
    session.forget();
    await unwrap(publicClient.POST("/api/auth/logout"));
  },

  async requestPasswordReset(email: string): Promise<void> {
    await unwrap(
      publicClient.POST("/api/auth/password-reset", { body: { email } }),
    );
  },

  async resetPassword(token: string, password: string): Promise<void> {
    await unwrap(
      publicClient.POST("/api/auth/password-reset/confirm", {
        body: { token, password },
      }),
    );
  },

  // me doubles as "is there a session": it rejects with a 401 ApiError
  // when there is none, without announcing a sign-out. It is also where a
  // page that loads signed in -- or comes back from a provider -- learns
  // the session's CSRF token.
  //
  // A session from before CSRF tokens reads but cannot write, so it counts
  // as none: the visitor is shown sign-in rather than a board whose first
  // change would fail.
  async me(): Promise<Profile> {
    const body = await unwrap(probeClient.GET("/api/auth/me"));
    if (!body.csrf_token) {
      session.forget();
      raise(
        new ApiError(
          401,
          "this session predates CSRF tokens; sign in again",
          "csrf",
        ),
      );
    }
    return session.remember(body);
  },

  async listMembers(): Promise<Member[]> {
    return (await unwrap(client.GET("/api/members"))).members;
  },

  // listMemberNames is the same response with the people who have left the
  // tenant: enough to name them, never to list them as members.
  async listMemberNames(): Promise<{ members: Member[]; former: Member[] }> {
    const { members, former } = await unwrap(client.GET("/api/members"));
    return { members, former };
  },

  // getAuthConfig is public and never 401s, but it is still read through
  // "public" mode rather than the default "session" one: there is no
  // session to refresh before there is even a sign-in form on screen.
  async getAuthConfig(): Promise<AuthConfig> {
    return await unwrap(publicClient.GET("/api/auth/config"));
  },

  // -- Login providers and the account's sign-in methods --

  // providerSignInURL is where a plain provider sign-in starts: a
  // navigation (a GET), never a fetch -- it redirects on to the provider.
  // next is a local path to come back to; the server re-checks it.
  providerSignInURL(provider: string, next?: string): string {
    const base = `${BASE_URL}/api/auth/login/${encodeURIComponent(provider)}`;
    return next ? `${base}?next=${encodeURIComponent(next)}` : base;
  },

  // The round trips that act on an account start from POSTs (which the
  // server refuses from another site's page), each answering where to send
  // the browser.
  async startInviteWithProvider(
    provider: string,
    token: string,
    next: string,
    tenantName?: string,
  ): Promise<string> {
    const body = await unwrap(
      publicClient.POST("/api/auth/invite/accept/{provider}", {
        params: { path: { provider } },
        body: { token, next, tenant_name: tenantName },
      }),
    );
    return body.redirect_url;
  },

  async startLink(provider: string, next: string): Promise<string> {
    const body = await unwrap(
      client.POST("/api/auth/link/{provider}", {
        params: { path: { provider } },
        body: { next },
      }),
    );
    return body.redirect_url;
  },

  async startReauth(provider: string, next: string): Promise<string> {
    const body = await unwrap(
      client.POST("/api/auth/reauth/{provider}", {
        params: { path: { provider } },
        body: { next },
      }),
    );
    return body.redirect_url;
  },

  async reauthWithPassword(password: string): Promise<void> {
    await unwrap(
      credentialClient.POST("/api/auth/reauth/password", {
        body: { password },
      }),
    );
  },

  async listSignInMethods(): Promise<SignInMethodsBody> {
    return await unwrap(client.GET("/api/auth/sign-in-methods"));
  },

  // setPassword adds a password or changes it; either way every other
  // session ends, and this one's cookies come back re-issued.
  async setPassword(password: string): Promise<void> {
    await unwrap(
      client.PUT("/api/auth/sign-in-methods/password", {
        body: { new_password: password },
      }),
    );
  },

  async removePassword(): Promise<void> {
    await unwrap(client.DELETE("/api/auth/sign-in-methods/password"));
  },

  // The identity is named by query parameters: an OIDC subject can hold a
  // slash.
  async removeIdentity(provider: string, subject: string): Promise<void> {
    await unwrap(
      client.DELETE("/api/auth/sign-in-methods/identity", {
        params: { query: { provider, subject } },
      }),
    );
  },

  // -- Invites --
  //
  // previewInvite/acceptInviteAsNewAccount are public (the invitee has no
  // session, and may have no account at all yet); the rest need one.

  async previewInvite(token: string): Promise<InvitePreview> {
    return await unwrap(
      publicClient.POST("/api/auth/invite/preview", { body: { token } }),
    );
  },

  // Creates the invitee's account and signs them in, a new session as
  // login()'s is -- the fresh session is exactly a sign-in from the app's
  // point of view.
  async acceptInviteAsNewAccount(
    token: string,
    displayName: string,
    password: string,
    tenantName?: string,
  ): Promise<Profile> {
    return session.signedIn(
      await unwrap(
        publicClient.POST("/api/auth/invite/accept", {
          body: {
            token,
            display_name: displayName,
            password,
            tenant_name: tenantName,
          },
        }),
      ),
    );
  },

  // acceptInvite is for someone already signed in accepting an invite for
  // their own address -- it moves the session into the invite's tenant, so
  // callers reload rather than trying to patch every already-fetched list
  // (tickets, agents, members) for the new tenant by hand.
  // tenantName names the tenant a new_tenant invite makes.
  async acceptInvite(token: string, tenantName?: string): Promise<Profile> {
    return session.tenantSwitched(
      await unwrap(
        client.POST("/api/invites/accept", {
          body: { token, tenant_name: tenantName },
        }),
      ),
    );
  },

  async createInvite(email: string): Promise<Invite> {
    return await unwrap(client.POST("/api/invites", { body: { email } }));
  },

  async listInvites(): Promise<Invite[]> {
    return (await unwrap(client.GET("/api/invites"))).invites;
  },

  async revokeInvite(id: string): Promise<void> {
    await unwrap(
      client.DELETE("/api/invites/{id}", { params: { path: { id } } }),
    );
  },

  async listTenants(): Promise<Tenant[]> {
    return (await unwrap(client.GET("/api/auth/tenants"))).tenants;
  },

  // tenantOverview is listTenants with how many tenants the caller may own
  // (0: creating their own is switched off), for the New tenant page.
  async tenantOverview(): Promise<TenantOverview> {
    const { tenants, owner_limit } = await unwrap(client.GET("/api/auth/tenants"));
    return { tenants, ownerLimit: owner_limit };
  },

  // createTenant makes a tenant the caller owns and moves the session into
  // it, as switchTenant does, so callers reload afterwards.
  async createTenant(name: string): Promise<Profile> {
    return session.tenantSwitched(
      await unwrap(client.POST("/api/tenants", { body: { name } })),
    );
  },

  // renameTenant renames the current tenant; the server refuses anyone who
  // is not its owner.
  async renameTenant(name: string): Promise<Tenant> {
    return await unwrap(client.PATCH("/api/tenant", { body: { name } }));
  },

  // setTenantColour sets the current tenant's colour, or with null clears it
  // to the default; the server refuses anyone who is not its owner.
  async setTenantColour(colour: Tenant["colour"]): Promise<Tenant> {
    return await unwrap(client.PUT("/api/tenant/colour", { body: { colour } }));
  },

  // switchTenant moves the session into another of the caller's tenants --
  // like acceptInvite, callers reload afterwards rather than reconciling
  // every board list against a tenant they were never fetched for.
  async switchTenant(tenantId: string): Promise<Profile> {
    return session.tenantSwitched(
      await unwrap(
        client.POST("/api/auth/switch-tenant", {
          body: { tenant_id: tenantId },
        }),
      ),
    );
  },

  // archiveTenant archives a tenant the caller owns, naming it back as the
  // server requires. A session in it moves to another of the caller's
  // tenants, so callers reload afterwards, as for switchTenant.
  async archiveTenant(tenantId: string, confirmName: string): Promise<Profile> {
    return session.tenantSwitched(
      await unwrap(
        client.POST("/api/tenants/{id}/archive", {
          params: { path: { id: tenantId } },
          body: { confirm_name: confirmName },
        }),
      ),
    );
  },

  async unarchiveTenant(tenantId: string): Promise<void> {
    await unwrap(
      client.POST("/api/tenants/{id}/unarchive", {
        params: { path: { id: tenantId } },
      }),
    );
  },

  // listArchivedTenants is the tenants the caller has archived; empty for
  // anyone who owns none.
  async listArchivedTenants(): Promise<Tenant[]> {
    return (await unwrap(client.GET("/api/tenants/archived"))).tenants;
  },

  // -- Admin invites --
  //
  // These invite someone to start a tenant of their own, not into the
  // caller's -- server/internal/user/routes.go gates all three on
  // authn.RequireAdmin, mirrored by nginx's auth_request in front of the
  // admin bundle these are called from.

  async createAdminInvite(email: string): Promise<Invite> {
    return await unwrap(client.POST("/api/admin/invites", { body: { email } }));
  },

  async listAdminInvites(): Promise<Invite[]> {
    return (await unwrap(client.GET("/api/admin/invites"))).invites;
  },

  async revokeAdminInvite(id: string): Promise<void> {
    await unwrap(
      client.DELETE("/api/admin/invites/{id}", { params: { path: { id } } }),
    );
  },
};
