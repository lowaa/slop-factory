import { screen } from "@testing-library/react";
import type { UserEvent } from "@testing-library/user-event";

// Helpers for driving @slop-factory/ui's Dropdown/FilterableDropdown from component tests.
//
// The combobox is looked up with findByRole because most of these pickers
// are populated by a fetch, so the control is not in the document on the
// first tick.
//
// These replaced native <select>s, and userEvent.selectOptions only works
// on a real select element -- so every picker in the app is now operated
// the way a person operates it: open the control, then click the option.
// One helper rather than that pair repeated in five test files.

export async function chooseFromDropdown(
  user: UserEvent,
  label: string | RegExp,
  optionName: string | RegExp,
): Promise<void> {
  await user.click(await screen.findByRole("combobox", { name: label }));
  await user.click(await screen.findByRole("option", { name: optionName }));
}

// Filtering first is the point of FilterableDropdown, so this types before
// picking -- a test that skipped straight to the option would not notice
// if filtering stopped working.
export async function filterAndChoose(
  user: UserEvent,
  label: string | RegExp,
  query: string,
  optionName: string | RegExp,
): Promise<void> {
  await user.type(await screen.findByRole("combobox", { name: label }), query);
  await user.click(await screen.findByRole("option", { name: optionName }));
}

// selectedValueOf reads what a Dropdown is currently showing, for the
// assertions that used to be `toHaveValue` on a select.
export function selectedValueOf(label: string | RegExp): string {
  return screen.getByRole("combobox", { name: label }).textContent ?? "";
}
