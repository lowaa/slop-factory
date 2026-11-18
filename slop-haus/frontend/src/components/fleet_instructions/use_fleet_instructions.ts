import { useCallback, useEffect, useState } from "react";
import { apiClient, type FleetInstructions, type FleetInstructionsKind } from "@/api";
import { errorMessage, useAsyncAction } from "@/lib/use_async_action";

export type FleetInstructionsState =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "loaded"; data: FleetInstructions };

export interface FleetInstructionsModel {
  state: FleetInstructionsState;
  busy: boolean;
  // The last action's failure; separate from state so a failed save leaves
  // the loaded instructions (and the draft) on screen.
  error: string | null;
  // Resolve true when the action went through, so a caller can clear its draft.
  save: (name: string, body: string, makeDefault: boolean) => Promise<boolean>;
  setDefault: (id: string | null) => Promise<boolean>;
  // Whether archived versions are listed. Off by default, so a tenant's
  // retired text stays out of the way until asked for.
  showArchived: boolean;
  setShowArchived: (show: boolean) => void;
  archiveVersion: (id: string) => Promise<boolean>;
  unarchiveVersion: (id: string) => Promise<boolean>;
  archiveGroup: (name: string) => Promise<boolean>;
  unarchiveGroup: (name: string) => Promise<boolean>;
}

// useFleetInstructions loads the tenant's fleet instructions and owns the
// actions on them. Each action reloads on success, so the view never edits
// what it shows.
export function useFleetInstructions(kind: FleetInstructionsKind = "fleet"): FleetInstructionsModel {
  const [state, setState] = useState<FleetInstructionsState>({ status: "loading" });
  const [reload, setReload] = useState(0);
  const [showArchived, setShowArchived] = useState(false);
  const { busy, error, run: runAction } = useAsyncAction();

  useEffect(() => {
    let cancelled = false;
    apiClient
      .getFleetInstructions(showArchived, kind)
      .then((data) => {
        if (!cancelled) setState({ status: "loaded", data });
      })
      .catch((err: unknown) => {
        if (!cancelled) {
          setState({
            status: "error",
            message: errorMessage(err, "Failed to load the fleet instructions."),
          });
        }
      });
    return () => {
      cancelled = true;
    };
  }, [reload, showArchived, kind]);

  const run = useCallback(
    async (action: () => Promise<unknown>) => {
      let ok = false;
      await runAction(async () => {
        await action();
        setReload((n) => n + 1);
        ok = true;
      }, "That did not work. Try again.");
      return ok;
    },
    [runAction],
  );

  const save = useCallback(
    (name: string, body: string, makeDefault: boolean) =>
      run(() => apiClient.saveFleetInstructionsVersion(name, body, makeDefault, kind)),
    [run, kind],
  );
  const setDefault = useCallback(
    (id: string | null) => run(() => apiClient.setFleetInstructionsDefault(id, kind)),
    [run, kind],
  );

  const archiveVersion = useCallback((id: string) => run(() => apiClient.archiveFleetInstructionsVersion(id)), [run]);
  const unarchiveVersion = useCallback((id: string) => run(() => apiClient.unarchiveFleetInstructionsVersion(id)), [run]);
  const archiveGroup = useCallback((name: string) => run(() => apiClient.archiveFleetInstructionsGroup(name, kind)), [run, kind]);
  const unarchiveGroup = useCallback((name: string) => run(() => apiClient.unarchiveFleetInstructionsGroup(name, kind)), [run, kind]);

  return {
    state,
    busy,
    error,
    save,
    setDefault,
    showArchived,
    setShowArchived,
    archiveVersion,
    unarchiveVersion,
    archiveGroup,
    unarchiveGroup,
  };
}
