import { useEffect, useRef } from "react";
import { useConsentContext } from "./consent_context";
import type { ScriptLoader } from "./script_gate";

interface ConsentScriptProps {
  purpose: string;
  // Names the script; see LoadOptions.id.
  id: string;
  load: ScriptLoader;
  onError?: (error: unknown) => void;
}

// Loads a script while `purpose` is granted, and tears it down on
// withdrawal or unmount. The only sanctioned way to load an optional
// script: see script_gate.ts, including what adding a vendor also needs.
export default function ConsentScript({ purpose, id, load, onError }: ConsentScriptProps) {
  const { store } = useConsentContext();
  // The latest loader without restarting the script when its identity changes.
  const loadRef = useRef(load);
  loadRef.current = load;
  const onErrorRef = useRef(onError);
  onErrorRef.current = onError;

  useEffect(
    () =>
      store.loadWhenConsented(purpose, (signal) => loadRef.current(signal), {
        id,
        onError: (e) => onErrorRef.current?.(e),
      }),
    [store, purpose, id],
  );
  return null;
}
