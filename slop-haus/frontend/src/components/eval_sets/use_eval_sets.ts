import { useCallback, useEffect, useState } from "react";
import { apiClient, type EvalSet, type EvalSetDefinition } from "@/api";
import { errorMessage, useAsyncAction } from "@/lib/use_async_action";

export type EvalSetsState =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "loaded"; sets: EvalSet[] };

export interface EvalSetsModel {
  state: EvalSetsState;
  busy: boolean;
  // The last action's failure; separate from state so the list stays up.
  error: string | null;
  clearError: () => void;
  // Each resolves to the saved set, or null once the failure is in error.
  create: (definition: EvalSetDefinition) => Promise<EvalSet | null>;
  replace: (id: string, definition: EvalSetDefinition) => Promise<EvalSet | null>;
  pin: (id: string) => Promise<EvalSet | null>;
  remove: (id: string) => Promise<boolean>;
}

// useEvalSets loads the tenant's eval sets and owns the actions on them;
// each reloads the list on success so the view never edits what it shows.
export function useEvalSets(): EvalSetsModel {
  const [state, setState] = useState<EvalSetsState>({ status: "loading" });
  const [reload, setReload] = useState(0);
  const { busy, error, run, setError } = useAsyncAction();

  useEffect(() => {
    let cancelled = false;
    apiClient
      .listEvalSets()
      .then((sets) => {
        if (!cancelled) setState({ status: "loaded", sets });
      })
      .catch((err: unknown) => {
        if (!cancelled) setState({ status: "error", message: errorMessage(err, "Failed to load the eval sets.") });
      });
    return () => {
      cancelled = true;
    };
  }, [reload]);

  const act = useCallback(
    async <T,>(task: () => Promise<T>, failure: string): Promise<T | null> => {
      let out: T | null = null;
      await run(async () => {
        out = await task();
        setReload((n) => n + 1);
      }, failure);
      return out;
    },
    [run],
  );

  return {
    state,
    busy,
    error,
    clearError: () => setError(null),
    create: (definition) => act(() => apiClient.createEvalSet(definition), "Failed to save the set."),
    replace: (id, definition) => act(() => apiClient.replaceEvalSet(id, definition), "Failed to save the set."),
    pin: (id) => act(() => apiClient.pinEvalSetMatches(id), "Failed to pin the matches."),
    remove: async (id) => (await act(async () => (await apiClient.deleteEvalSet(id), true), "Failed to delete the set.")) === true,
  };
}
