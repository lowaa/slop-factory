import { useCallback, useEffect, useRef, useState } from "react";
import { apiClient, type Grant } from "@/api";
import { errorMessage } from "@/lib/use_async_action";

export type GrantsState =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "loaded"; grants: Grant[] };

// useGrants holds the tenant's live authorizations and revokes them one at a
// time. The grants are shown on the row of the agent each acts as, so every
// page with such rows shares this.
export function useGrants() {
  const [state, setState] = useState<GrantsState>({ status: "loading" });
  const [revokingId, setRevokingId] = useState<string | null>(null);
  const [revokeError, setRevokeError] = useState<{ grantId: string; message: string } | null>(null);

  // Only the latest listing may land: a refresh after a revoke can overtake
  // the one started on mount, and letting the older response arrive last
  // would show grants that have already been revoked.
  const request = useRef(0);
  const refresh = useCallback(() => {
    const mine = ++request.current;
    return apiClient
      .listGrants()
      .then((grants) => {
        if (mine === request.current) setState({ status: "loaded", grants });
      })
      .catch((err: unknown) => {
        if (mine !== request.current) return;
        setState({
          status: "error",
          message: errorMessage(err, "Failed to load authorizations."),
        });
      });
  }, []);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  async function revoke(id: string) {
    setRevokeError(null);
    setRevokingId(id);
    try {
      await apiClient.revokeGrant(id);
      await refresh();
    } catch (err) {
      setRevokeError({ grantId: id, message: errorMessage(err, "Failed to revoke authorization.") });
    } finally {
      setRevokingId(null);
    }
  }

  return { state, refresh, revoke, revokingId, revokeError };
}

export type GrantsHandle = ReturnType<typeof useGrants>;

// grantsOf is the loaded grants acting as one agent; none while loading.
export function grantsOf(handle: GrantsHandle, agentId: string): Grant[] {
  return handle.state.status === "loaded" ? handle.state.grants.filter((g) => g.agent_id === agentId) : [];
}
