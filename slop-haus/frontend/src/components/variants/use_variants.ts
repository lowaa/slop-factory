import { useCallback, useEffect, useState } from "react";
import {
  apiClient,
  ApiError,
  type FleetInstructionsVersion,
  type HarnessSettingDefinition,
  type VariantVersion,
} from "@/api";
import { errorMessage, useAsyncAction } from "@/lib/use_async_action";

export interface VariantsData {
  versions: VariantVersion[];
  definitions: HarnessSettingDefinition[];
  // Every fleet instructions version, archived too: a variant keeps the one
  // it pinned after that is archived, and the list names it.
  fleetVersions: FleetInstructionsVersion[];
}

export type VariantsState =
  | { status: "loading" }
  | { status: "error"; message: string }
  // The server refuses the list to anyone but an owner; that refusal is how
  // the board learns the viewer is not one.
  | { status: "forbidden" }
  | { status: "loaded"; data: VariantsData };

export interface VariantsModel {
  state: VariantsState;
  busy: boolean;
  // The last archive action's failure; separate from state so it leaves the
  // loaded list on screen.
  error: string | null;
  // Rejects with the server's refusal, for the save form to show beside its
  // fields.
  save: (
    name: string,
    harness: VariantVersion["harness"],
    settings: Record<string, string>,
    fleetVersionId: string | null,
  ) => Promise<void>;
  showArchived: boolean;
  setShowArchived: (show: boolean) => void;
  archiveVersion: (id: string) => Promise<boolean>;
  unarchiveVersion: (id: string) => Promise<boolean>;
  archiveGroup: (name: string) => Promise<boolean>;
  unarchiveGroup: (name: string) => Promise<boolean>;
}

// useVariants loads the tenant's variant versions and owns the actions on
// them. Each action reloads on success, so the view never edits what it
// shows.
export function useVariants(): VariantsModel {
  const [state, setState] = useState<VariantsState>({ status: "loading" });
  const [reload, setReload] = useState(0);
  const [showArchived, setShowArchived] = useState(false);
  const { busy, error, run: runAction } = useAsyncAction();

  useEffect(() => {
    let cancelled = false;
    Promise.all([
      apiClient.listVariants(showArchived),
      apiClient.getHarnessConfig(),
      apiClient.getFleetInstructions(true),
    ])
      .then(([versions, config, fleet]) => {
        if (cancelled) return;
        setState({
          status: "loaded",
          data: { versions, definitions: config.definitions, fleetVersions: fleet.versions },
        });
      })
      .catch((err: unknown) => {
        if (cancelled) return;
        if (err instanceof ApiError && err.status === 403) {
          setState({ status: "forbidden" });
          return;
        }
        setState({
          status: "error",
          message: errorMessage(err, "Failed to load the variants."),
        });
      });
    return () => {
      cancelled = true;
    };
  }, [reload, showArchived]);

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
    async (
      name: string,
      harness: VariantVersion["harness"],
      settings: Record<string, string>,
      fleetVersionId: string | null,
    ) => {
      await apiClient.saveVariantVersion(name, harness, settings, fleetVersionId);
      setReload((n) => n + 1);
    },
    [],
  );

  const archiveVersion = useCallback((id: string) => run(() => apiClient.archiveVariantVersion(id)), [run]);
  const unarchiveVersion = useCallback((id: string) => run(() => apiClient.unarchiveVariantVersion(id)), [run]);
  const archiveGroup = useCallback((name: string) => run(() => apiClient.archiveVariantGroup(name)), [run]);
  const unarchiveGroup = useCallback((name: string) => run(() => apiClient.unarchiveVariantGroup(name)), [run]);

  return { state, busy, error, save, showArchived, setShowArchived, archiveVersion, unarchiveVersion, archiveGroup, unarchiveGroup };
}
