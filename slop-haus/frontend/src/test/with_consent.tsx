import type { ReactNode } from "react";
import { ConsentProvider, type ConsentStore } from "@slop-factory/privacy/consent";
import { legalDocHref } from "@/privacy/legal_links";
import { PURPOSE_COPY } from "@/privacy/purpose_copy";
import { makeConsentStore } from "./consent";

export function WithConsent({ store, children }: { store?: ConsentStore; children: ReactNode }) {
  return (
    <ConsentProvider store={store ?? makeConsentStore()} documentHref={legalDocHref} copy={PURPOSE_COPY}>
      {children}
    </ConsentProvider>
  );
}
