import { describe, expect, it } from "vitest";
import { act, fireEvent, renderHook } from "@testing-library/react";
import { useBulkSelection } from "./use_bulk_selection";

function renderSelection(present: readonly number[] | undefined = [1, 2, 3]) {
  return renderHook(({ keys }) => useBulkSelection<number>(keys), {
    initialProps: { keys: present as readonly number[] | undefined },
  });
}

function selectedOf(result: { current: { selected: ReadonlySet<number> } }): number[] {
  return [...result.current.selected].sort();
}

describe("useBulkSelection", () => {
  it("toggles a key in and out", () => {
    const { result } = renderSelection();
    act(() => result.current.enter());

    act(() => result.current.toggle(2));
    expect(selectedOf(result)).toEqual([2]);

    act(() => result.current.toggle(2));
    expect(selectedOf(result)).toEqual([]);
  });

  it("selects all of a column's keys on top of what is already selected, and deselects just those", () => {
    const { result } = renderSelection([1, 2, 3, 4]);
    act(() => result.current.enter());
    act(() => result.current.toggle(1));

    act(() => result.current.selectAll([3, 4]));
    expect(selectedOf(result)).toEqual([1, 3, 4]);

    act(() => result.current.deselect([3, 4]));
    expect(selectedOf(result)).toEqual([1]);
  });

  it("drops keys that leave the list, and keeps them while the list is unknown", () => {
    const { result, rerender } = renderSelection([1, 2, 3]);
    act(() => result.current.enter());
    act(() => result.current.selectAll([1, 2, 3]));

    rerender({ keys: undefined });
    expect(selectedOf(result)).toEqual([1, 2, 3]);

    rerender({ keys: [1, 3, 9] });
    expect(selectedOf(result)).toEqual([1, 3]);
    expect(result.current.active).toBe(true);

    // Dropped for good: coming back does not reselect it.
    rerender({ keys: [1, 2, 3] });
    expect(selectedOf(result)).toEqual([1, 3]);
  });

  it("leaves select mode and forgets the selection on Esc", () => {
    const { result } = renderSelection();
    act(() => result.current.enter());
    act(() => result.current.toggle(1));

    fireEvent.keyDown(document, { key: "Escape" });

    expect(result.current.active).toBe(false);
    expect(selectedOf(result)).toEqual([]);
  });

  // An open dropdown closes on Esc and preventDefaults it; that Esc is the
  // dropdown's, not a request to drop the whole selection.
  it("ignores an Esc something else already handled", () => {
    const { result } = renderSelection();
    act(() => result.current.enter());
    act(() => result.current.toggle(1));

    const handled = new KeyboardEvent("keydown", { key: "Escape", cancelable: true, bubbles: true });
    handled.preventDefault();
    act(() => void document.dispatchEvent(handled));

    expect(result.current.active).toBe(true);
    expect(selectedOf(result)).toEqual([1]);
  });

  it("leaves select mode with exit, which clears it too", () => {
    const { result } = renderSelection();
    act(() => result.current.enter());
    act(() => result.current.toggle(3));

    act(() => result.current.exit());
    act(() => result.current.enter());

    expect(result.current.active).toBe(true);
    expect(selectedOf(result)).toEqual([]);
  });
});
