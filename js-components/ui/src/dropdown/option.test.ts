import { describe, expect, it } from "vitest";
import {
  firstEnabledIndex,
  lastEnabledIndex,
  nextEnabledIndex,
  optionId,
  type DropdownOption,
} from "./option";

const options: DropdownOption[] = [
  { value: "a", label: "A", disabled: true },
  { value: "b", label: "B" },
  { value: "c", label: "C", disabled: true },
  { value: "d", label: "D" },
  { value: "e", label: "E", disabled: true },
];

describe("optionId", () => {
  it("builds an id from the list id and index", () => {
    expect(optionId("list-1", 3)).toBe("list-1-option-3");
  });
});

describe("firstEnabledIndex", () => {
  it("skips leading disabled options", () => {
    expect(firstEnabledIndex(options)).toBe(1);
  });

  it("returns -1 when every option is disabled", () => {
    expect(firstEnabledIndex([{ value: "a", label: "A", disabled: true }])).toBe(-1);
  });

  it("returns -1 for an empty list", () => {
    expect(firstEnabledIndex([])).toBe(-1);
  });
});

describe("lastEnabledIndex", () => {
  it("skips trailing disabled options", () => {
    expect(lastEnabledIndex(options)).toBe(3);
  });

  it("returns -1 when every option is disabled", () => {
    expect(lastEnabledIndex([{ value: "a", label: "A", disabled: true }])).toBe(-1);
  });

  it("returns -1 for an empty list", () => {
    expect(lastEnabledIndex([])).toBe(-1);
  });
});

describe("nextEnabledIndex", () => {
  it("steps forward over disabled options", () => {
    expect(nextEnabledIndex(options, 1, 1)).toBe(3);
  });

  it("steps backward over disabled options", () => {
    expect(nextEnabledIndex(options, 3, -1)).toBe(1);
  });

  // The no-wrap guarantee: holding ArrowDown parks on the last enabled
  // option rather than cycling back to the top.
  it("stays put at the end rather than wrapping", () => {
    expect(nextEnabledIndex(options, 3, 1)).toBe(3);
  });

  it("stays put at the start rather than wrapping", () => {
    expect(nextEnabledIndex(options, 1, -1)).toBe(1);
  });
});
