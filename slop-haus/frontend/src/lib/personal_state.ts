// Browser-side state that belongs to whoever is signed in: pending invites,
// unsent drafts, remembered filters. Features register what they hold when
// their module loads, so the auth context can forget (and bind) it without
// importing any feature.
//
// Registering at module load is enough: a module that was never loaded has
// nothing to forget. Keyed by name so a re-evaluated module (hot reload,
// vi.resetModules) replaces its entry instead of stacking a second one.
export interface PersonalState {
  forget(): void;
  // Called once the person is known, and safe to repeat.
  bindUser?(userId: string): void;
}

const registered = new Map<string, PersonalState>();

export function registerPersonalState(name: string, state: PersonalState): void {
  registered.set(name, state);
}

export function forgetPersonalState(): void {
  for (const s of registered.values()) s.forget();
}

export function bindPersonalStateUser(userId: string): void {
  for (const s of registered.values()) s.bindUser?.(userId);
}
