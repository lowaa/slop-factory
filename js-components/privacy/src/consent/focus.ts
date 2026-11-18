// An element anywhere in the app that opens the preferences dialog marks
// itself with this, so focus has somewhere sensible to go when the banner
// that held it is gone.
export const PREFERENCES_TRIGGER_ATTRIBUTE = "data-consent-preferences-trigger";

export function focusPreferencesTrigger(): void {
  document.querySelector<HTMLElement>(`[${PREFERENCES_TRIGGER_ATTRIBUTE}]`)?.focus();
}

const FOCUSABLE = 'a[href], button:not([disabled]), input:not([disabled]), [tabindex]:not([tabindex="-1"])';

export function focusableIn(root: HTMLElement): HTMLElement[] {
  return [...root.querySelectorAll<HTMLElement>(FOCUSABLE)];
}
