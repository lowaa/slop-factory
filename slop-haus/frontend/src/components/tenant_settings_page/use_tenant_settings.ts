import { apiClient } from "@/api";
import { resourceKeys, useResource } from "@/lib/resource";

// The page's sections read one list. It is not kept once the page closes: the
// header's switch changes the processing setting while nothing here is
// listening, so opening the page again must read afresh.
export function useTenantSettings() {
  return useResource(resourceKeys.tenantSettings(), () => apiClient.listSettings(), {
    fallbackError: "Failed to load this tenant's settings.",
    keepUnused: false,
  });
}
