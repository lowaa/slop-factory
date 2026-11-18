import { expect } from "vitest";

// The message box and Send are one row, the box first: they share a
// message-composer__row form, which lays the box and the actions group
// holding Send out in a row (the e2e spec checks the pixels, which jsdom has
// none of).
export function expectOneRow(box: HTMLElement, send: HTMLElement): void {
  const form = box.closest("form.message-composer__row");
  expect(form).not.toBeNull();
  const actions = send.closest(".message-composer-actions");
  expect(actions).not.toBeNull();
  expect(Array.from(form!.children)).toEqual([box, actions]);
}
