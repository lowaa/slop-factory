import { useCallback, useEffect, useMemo, useState, useSyncExternalStore } from "react";
import type { ReactNode } from "react";
import type { LegalDocHref } from "../legal_docs/types";
import { ConsentContext, useConsentContext, type ConsentContextValue } from "./consent_context";
import { ConsentStore } from "./consent_store";
import type { PurposeCopy } from "./purpose_copy";
import type { DecisionSource } from "./types";
import ConsentBanner from "./consent_banner";
import PreferencesDialog from "./preferences_dialog";
import "./consent_provider.scss";

export interface ConsentProviderProps {
  // Create it with `new ConsentStore(...)` outside React when something
  // else needs the same one before the tree mounts -- the logger's consent
  // check, a script gate started at boot. Otherwise the provider makes its own.
  store?: ConsentStore;
  // Where the cookie policy (and any other legal document) lives in the app.
  documentHref?: LegalDocHref;
  // The display name and description per purpose id: legal copy, from the
  // app's legal documents. Falls back to a placeholder for known ids.
  copy?: Readonly<Record<string, PurposeCopy>>;
  // Renders the banner and the preferences dialog itself, which is almost
  // always wanted. Off for an app that places them (or one of them) itself.
  ui?: boolean;
  children?: ReactNode;
}

export function ConsentProvider({ store: given, documentHref, copy, ui = true, children }: ConsentProviderProps) {
  const [owned] = useState(() => (given ? null : new ConsentStore()));
  const store = given ?? (owned as ConsentStore);
  const snapshot = useSyncExternalStore(store.subscribe, store.getSnapshot, store.getSnapshot);
  const [preferencesOpen, setPreferencesOpen] = useState(false);
  const [preferencesSource, setPreferencesSource] = useState<DecisionSource>("settings");

  useEffect(() => store.start(), [store]);

  const openPreferences = useCallback((source: DecisionSource = "settings") => {
    setPreferencesSource(source);
    setPreferencesOpen(true);
  }, []);
  const closePreferences = useCallback(() => setPreferencesOpen(false), []);
  const value = useMemo<ConsentContextValue>(
    () => ({ store, snapshot, documentHref, copy, preferencesOpen, preferencesSource, openPreferences, closePreferences }),
    [store, snapshot, documentHref, copy, preferencesOpen, preferencesSource, openPreferences, closePreferences],
  );

  return (
    <ConsentContext.Provider value={value}>
      {children}
      {ui && (
        <>
          <ConsentBanner />
          <PreferencesDialog />
        </>
      )}
      <SavedAnnouncer />
    </ConsentContext.Provider>
  );
}

// Tells a screen reader a choice was saved: the banner that carried the
// button disappears, which would otherwise be silent.
function SavedAnnouncer() {
  const { snapshot } = useConsentContext();
  // A live region only announces a change, so a second save alternates a
  // trailing space to still be read out.
  const message = snapshot.savedCount > 0 ? `Your cookie preferences were saved.${snapshot.savedCount % 2 ? "" : "\u00a0"}` : "";
  return (
    // aria-live rather than role="status": an always-present status region
    // would be a second "status" on every page, which an app's own status
    // messages (and its tests' role queries) would have to tell apart from.
    <div aria-live="polite" aria-atomic="true" className="consent-visually-hidden" data-testid="consent-saved-announcer">
      {message}
    </div>
  );
}

