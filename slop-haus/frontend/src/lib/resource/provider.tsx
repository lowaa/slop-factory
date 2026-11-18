import type { ReactNode } from "react";
import { ResourceClientContext, defaultResourceClient, type ResourceClient } from "./client";

export function ResourceProvider({ client, children }: { client?: ResourceClient; children: ReactNode }) {
  return <ResourceClientContext.Provider value={client ?? defaultResourceClient}>{children}</ResourceClientContext.Provider>;
}
