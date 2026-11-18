// The header's switch and the Tenant settings page both read and write the
// one ticket-processing setting, but each holds its own copy of it. Whichever
// writes announces the new value here so the other, if it is mounted, shows
// it too without a reload.
type Listener = (enabled: boolean, origin: symbol) => void;

const listeners = new Set<Listener>();

// origin lets a component skip its own announcement, which it has already
// applied.
export function announceProcessing(enabled: boolean, origin: symbol): void {
  for (const listener of [...listeners]) listener(enabled, origin);
}

export function onProcessingAnnounced(listener: Listener): () => void {
  listeners.add(listener);
  return () => void listeners.delete(listener);
}
