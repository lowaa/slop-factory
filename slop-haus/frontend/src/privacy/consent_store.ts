import { ConsentStore, createFetchConsentApi } from "@slop-factory/privacy/consent";
import { consentFetch } from "@/api";

// One per page, made before the tree mounts so the logger's consent check
// can use it too (see main.tsx). The name keys the cross-tab lock.
export function createConsentStore(): ConsentStore {
  return new ConsentStore({ api: createFetchConsentApi({ fetch: consentFetch }), name: "slop-haus-consent" });
}
