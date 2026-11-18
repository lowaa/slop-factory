import { createContext, useContext, useEffect, useMemo, useState, type ReactNode } from "react";
import { onApiFailure, onTenantChanged } from "@/api";

// What has made this tab's session unusable, so the shell replaces the tab.
// Archived wins over changed: the tenant the tab is for cannot be used at all.
export type SessionInterrupt = { kind: "archived" } | { kind: "changed"; was: string } | null;

const SessionInterruptContext = createContext<SessionInterrupt>(null);

// The server refuses every request of an archived tenant's session with
// tenant_archived, so any request may be the one that tells the tab.
export function SessionInterruptProvider({ children }: { children: ReactNode }) {
  const [archived, setArchived] = useState(false);
  const [was, setWas] = useState<string | null>(null);
  useEffect(
    () =>
      onApiFailure((failure) => {
        if (failure.code === "tenant_archived") setArchived(true);
      }),
    [],
  );
  useEffect(() => onTenantChanged(setWas), []);
  const interrupt = useMemo<SessionInterrupt>(
    () => (archived ? { kind: "archived" } : was ? { kind: "changed", was } : null),
    [archived, was],
  );
  return <SessionInterruptContext.Provider value={interrupt}>{children}</SessionInterruptContext.Provider>;
}

export function useSessionInterrupt(): SessionInterrupt {
  return useContext(SessionInterruptContext);
}
