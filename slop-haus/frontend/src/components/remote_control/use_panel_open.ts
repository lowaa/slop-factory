import { usePersisted } from "@slop-factory/storage/react";
import { browserStorage, type BrowserStorage } from "@/lib/browser_storage";
import { useNarrowLayout } from "@/lib/layout";

// usePanelOpen is whether the sessions panel is open, per device: what the
// person last chose is kept in the browser, and until they choose it is open
// on a wide screen and closed on a phone, where it would take the page.
export function usePanelOpen(storage: BrowserStorage = browserStorage()): [boolean, (open: boolean) => void] {
  const narrow = useNarrowLayout();
  const { store, value } = storage.sessionsPanelOpen(!narrow);
  return usePersisted(store, value);
}
