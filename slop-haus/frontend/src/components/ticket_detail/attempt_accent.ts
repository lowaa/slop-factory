import "./attempt_accent.scss";

// Matches the --attempt-N tokens in index.scss and the loop in
// attempt_accent.scss.
export const ATTEMPT_ACCENT_COUNT = 7;

// Colour is fixed by attempt number, so attempt 3 is the same colour on
// every ticket. Past the palette it wraps; the number is always shown beside
// the colour, so the rare clash is still readable.
export function attemptAccentIndex(attemptNumber: number): number {
  const zeroBased = (attemptNumber - 1) % ATTEMPT_ACCENT_COUNT;
  return ((zeroBased + ATTEMPT_ACCENT_COUNT) % ATTEMPT_ACCENT_COUNT) + 1;
}

// Sets --attempt-accent for the element and its descendants.
export function attemptAccentClass(attemptNumber: number): string {
  return `attempt-accent-${attemptAccentIndex(attemptNumber)}`;
}
