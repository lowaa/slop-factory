import { createContext, useCallback, useContext, useMemo } from "react";
import type { LegalDocHref } from "../legal_docs/types";
import type { ConsentStore, ConsentSnapshot } from "./consent_store";
import { purposeCopy, type PurposeCopy } from "./purpose_copy";
import type { Decision, DecisionSource, PurposeState } from "./types";

export interface ConsentContextValue {
  store: ConsentStore;
  snapshot: ConsentSnapshot;
  documentHref: LegalDocHref | undefined;
  copy: Readonly<Record<string, PurposeCopy>> | undefined;
  preferencesOpen: boolean;
  // What a decision made in the dialog is recorded as having come through.
  preferencesSource: DecisionSource;
  openPreferences: (source?: DecisionSource) => void;
  closePreferences: () => void;
}

export const ConsentContext = createContext<ConsentContextValue | null>(null);

export function useConsentContext(): ConsentContextValue {
  const ctx = useContext(ConsentContext);
  if (!ctx) throw new Error("useConsent must be used inside a ConsentProvider");
  return ctx;
}

export interface UseConsent {
  // The server reports this purpose. A purpose behind a capability flag that
  // is off is not reported, and so isn't offered or granted.
  available: boolean;
  granted: boolean;
  needsDecision: boolean;
  loading: boolean;
  decide: (granted: boolean, source?: DecisionSource) => Promise<boolean>;
}

export function useConsent(purposeId: string): UseConsent {
  const { store, snapshot } = useConsentContext();
  const purpose = snapshot.purposes.find((p) => p.id === purposeId);
  const decide = useCallback(
    (granted: boolean, source: DecisionSource = "settings") => store.decide(source, [{ purpose: purposeId, granted }]),
    [store, purposeId],
  );
  return {
    available: purpose !== undefined,
    granted: purpose?.granted ?? false,
    needsDecision: purpose?.needsDecision ?? false,
    loading: snapshot.status === "loading",
    decide,
  };
}

export function useConsentSnapshot(): ConsentSnapshot {
  return useConsentContext().snapshot;
}

export function useConsentActions() {
  const { store, openPreferences, closePreferences, preferencesOpen, preferencesSource } = useConsentContext();
  return useMemo(
    () => ({
      acceptAll: (source: DecisionSource = "banner") => store.decideAll(source, true),
      rejectAll: (source: DecisionSource = "banner") => store.decideAll(source, false),
      decide: (source: DecisionSource, decisions: readonly Decision[]) => store.decide(source, decisions),
      openPreferences,
      closePreferences,
      preferencesOpen,
      preferencesSource,
    }),
    [store, openPreferences, closePreferences, preferencesOpen, preferencesSource],
  );
}

export function usePurposeCopy(): (purpose: PurposeState) => PurposeCopy {
  const { copy } = useConsentContext();
  return useCallback((p: PurposeState) => purposeCopy(p.id, copy), [copy]);
}

export function useDocumentHref(): LegalDocHref | undefined {
  return useConsentContext().documentHref;
}

// The logger's check, as a stable function that reads the live state:
// `new OtelLogger({ hasAnalyticsConsent: useConsentCheck("analytics") ... })`.
// Outside React, `store.check("analytics")` is the same function.
export function useConsentCheck(purposeId: string): () => boolean {
  const { store } = useConsentContext();
  return useMemo(() => store.check(purposeId), [store, purposeId]);
}
