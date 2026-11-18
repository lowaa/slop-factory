export { ConsentStore, purposesNeedingBanner, type ConsentSnapshot, type ConsentStoreOptions } from "./consent_store";
export { createFetchConsentApi, DEFAULT_CONSENT_PATH, type FetchConsentApiOptions } from "./consent_api";
export { ConsentProvider, type ConsentProviderProps } from "./consent_provider";
export {
  useConsent,
  useConsentActions,
  useConsentCheck,
  useConsentSnapshot,
  type UseConsent,
} from "./consent_context";
export { default as ConsentBanner } from "./consent_banner";
export { default as PreferencesDialog } from "./preferences_dialog";
export { default as CookiePreferencesButton } from "./cookie_preferences_button";
export { default as DoNotSellButton } from "./do_not_sell_button";
export { default as ConsentSettings } from "./consent_settings";
export { default as ConsentScript } from "./consent_script";
export { ScriptGate, type ConsentSource, type LoadOptions, type ScriptLoader, type Teardown } from "./script_gate";
export { DEFAULT_PURPOSE_COPY, type PurposeCopy } from "./purpose_copy";
export { readGpcSignal } from "./gpc";
export {
  ConsentApiError,
  type ConsentApi,
  type ConsentErrorCode,
  type ConsentState,
  type Decision,
  type DecisionSource,
  type PurposeKind,
  type PurposeState,
} from "./types";

import type { ConsentStore } from "./consent_store";
import type { LoadOptions, ScriptLoader } from "./script_gate";

// The sanctioned way to load an optional script from outside React (a boot
// file, a vendor wrapper). See ScriptGate. Returns a function that stops it.
export function loadWhenConsented(
  store: ConsentStore,
  purposeId: string,
  loader: ScriptLoader,
  options?: LoadOptions,
): () => void {
  return store.loadWhenConsented(purposeId, loader, options);
}
