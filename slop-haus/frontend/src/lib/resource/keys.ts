// Every resource key the app uses, in one place, so two views that want the
// same data cannot spell its key differently and silently fetch it twice.
export type ResourceKey = readonly [string, ...unknown[]];

export const resourceKeys = {
  tenants: () => ["tenants"] as const,
  // The tenants as they are now, never the cached list: for a view whose whole
  // point is that the session moved since the cache was filled.
  tenantsNow: () => ["tenants", "now"] as const,
  archivedTenants: () => ["tenants", "archived"] as const,
  // Archived agents are a different list from the default one, so they are
  // different keys; invalidating ["agents"] refreshes both.
  agents: (opts: { archived: boolean }) => ["agents", { archived: opts.archived }] as const,
  // The tenant's server settings, read by the tenant settings page's sections.
  tenantSettings: () => ["settings"] as const,
  members: () => ["members"] as const,
  memberNames: () => ["members", "names"] as const,
  // The ticket page's four reads. They share the ["ticket", id] prefix, so
  // one invalidate refreshes the page.
  ticket: (id: number) => ["ticket", id] as const,
  ticketComments: (id: number) => ["ticket", id, "comments"] as const,
  ticketContexts: (id: number) => ["ticket", id, "contexts"] as const,
  ticketRevisions: (id: number) => ["ticket", id, "revisions"] as const,
  // One attempt's slop-review runs, under the ticket's prefix so the page's
  // invalidation refreshes them too.
  ticketReviewRuns: (id: number, attempt: number) => ["ticket", id, "attempt", attempt, "review-runs"] as const,
  // usePolledResource's own keys: one per hook instance and deps generation,
  // so its views keep their start-over-on-deps semantics.
  polled: (instance: string, generation: number) => ["polled", instance, generation] as const,
} satisfies Record<string, (...args: never[]) => ResourceKey>;
