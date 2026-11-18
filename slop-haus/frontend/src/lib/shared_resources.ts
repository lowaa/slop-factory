import { apiClient } from "@/api";
import { readResource, resourceKeys, useResource, type ResourceOptions } from "./resource";

// The reads several views share. Each has one key and one fetcher here, so
// views asking for the same data share one request, and a writer refreshes
// every reader with invalidate(resourceKeys.x()).

export function useTenants(options?: ResourceOptions) {
  return useResource(resourceKeys.tenants(), () => apiClient.listTenants(), options);
}

export function useTenantsNow(options?: ResourceOptions) {
  return useResource(resourceKeys.tenantsNow(), () => apiClient.listTenants(), { ...options, keepUnused: false });
}

export function useArchivedTenants(options?: ResourceOptions) {
  return useResource(resourceKeys.archivedTenants(), () => apiClient.listArchivedTenants(), options);
}

export function useAgents(opts: { archived: boolean }, options?: ResourceOptions) {
  return useResource(resourceKeys.agents(opts), () => apiClient.listAgents({ includeArchived: opts.archived }), options);
}

export function useMembers(options?: ResourceOptions) {
  return useResource(resourceKeys.members(), () => apiClient.listMembers(), options);
}

export function useMemberNames(options?: ResourceOptions) {
  return useResource(resourceKeys.memberNames(), () => apiClient.listMemberNames(), options);
}

// For code that is not a view (a one-off list fetched when a page opens):
// answers from the same key and shares a request already in flight.
export function readAgents(opts: { archived: boolean }) {
  return readResource(resourceKeys.agents(opts), () => apiClient.listAgents({ includeArchived: opts.archived }));
}
