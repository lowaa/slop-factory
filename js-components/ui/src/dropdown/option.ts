import type { ReactNode } from "react";

export interface DropdownOption<T extends string = string> {
  value: T;
  label: string;
  // Secondary text right-aligned in the option row (e.g. a status or
  // category). Also searched by FilterableDropdown, not just displayed.
  hint?: string;
  // Decoration before the label (a badge, an icon). It is drawn aria-hidden,
  // so the option is still named by its label and hint alone.
  leading?: ReactNode;
  disabled?: boolean;
}

// Recomputed independently by the trigger's aria-activedescendant and by
// the panel's <li id>, so the two must agree exactly -- hence one helper
// rather than the template inlined in both places.
export function optionId(listId: string, index: number): string {
  return `${listId}-option-${index}`;
}

export function firstEnabledIndex<T extends string>(options: DropdownOption<T>[]): number {
  return options.findIndex((o) => !o.disabled);
}

export function lastEnabledIndex<T extends string>(options: DropdownOption<T>[]): number {
  for (let i = options.length - 1; i >= 0; i--) {
    if (!options[i].disabled) return i;
  }
  return -1;
}

// Deliberately does not wrap: running off either end returns `from`
// unchanged, so holding ArrowDown parks on the last enabled option rather
// than cycling back to the top. Wrapping makes a long catalog feel like it
// lost your place.
export function nextEnabledIndex<T extends string>(
  options: DropdownOption<T>[],
  from: number,
  step: 1 | -1,
): number {
  for (let i = from + step; i >= 0 && i < options.length; i += step) {
    if (!options[i].disabled) return i;
  }
  return from;
}
