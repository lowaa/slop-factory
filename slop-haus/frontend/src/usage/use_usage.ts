import { createContext, useContext } from "react";
import type { CallSiteAttrs, CallSiteEvent, Usage } from "./usage";

export interface CallSiteUsage {
  event<E extends CallSiteEvent>(name: E, attrs: CallSiteAttrs<E>): void;
}

export const UsageContext = createContext<Usage | null>(null);

// Outside a provider (a component rendered on its own in a test or a story)
// events go nowhere, rather than every such render needing the whole app.
const NO_USAGE: CallSiteUsage = { event: () => {} };

// useUsage is how a screen reports what was done on it. Only the events and
// attributes of the catalog compile, and the screen is added for it.
export function useUsage(): CallSiteUsage {
  return useContext(UsageContext) ?? NO_USAGE;
}
