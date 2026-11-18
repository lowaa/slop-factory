import { describe, expect, it } from "vitest";
import {
  ENVELOPE_WARNING,
  levelChangeConfirmation,
  STEWARD_CATEGORIES,
  stewardWarnings,
  tierAtLevel,
  tierChangeConfirmation,
  type StewardCategory,
} from "./steward_settings";

const judgment: StewardCategory = {
  id: "review_approve",
  label: "Approve a review",
  neverAuto: false,
  class: "judgment",
};
const base = {
  overrides: {},
  categories: [...STEWARD_CATEGORIES, judgment],
  record: () => ({ assessed: 100, agreementPct: 100 }),
  minDecisions: 20,
  minAgreementPct: 95,
  protectedPathRules: 2,
  sensitiveWordsEmpty: false,
} as const;

describe("tierAtLevel", () => {
  it("gives judgment categories human, veto, auto at levels 3, 4, 5 and lets an override win", () => {
    expect([3, 4, 5].map((l) => tierAtLevel(judgment, l as 3 | 4 | 5, {}))).toEqual(["human", "veto", "auto"]);
    expect(tierAtLevel(judgment, 5, { review_approve: "human" })).toBe("human");
    expect(tierAtLevel(judgment, 2, { review_approve: "auto" })).toBe("human");
  });
});

describe("stewardWarnings", () => {
  it("warns at level 5 and for an auto judgment category with the envelope sentence", () => {
    expect(stewardWarnings({ ...base, level: 5 })).toContain(ENVELOPE_WARNING);
    expect(
      stewardWarnings({
        ...base,
        level: 3,
        overrides: { review_approve: "auto" },
      }),
    ).toContain(ENVELOPE_WARNING);
    expect(stewardWarnings({ ...base, level: 4 })).not.toContain(ENVELOPE_WARNING);
  });

  it("warns about no protected paths only when a judgment category is above human, and not when unknown", () => {
    const none = (level: 3 | 4, protectedPathRules: number | null) =>
      stewardWarnings({ ...base, level, protectedPathRules }).some((w) => w.includes("No protected paths"));
    expect(none(4, 0)).toBe(true);
    expect(none(3, 0)).toBe(false);
    expect(none(4, null)).toBe(false);
    expect(none(4, 1)).toBe(false);
  });

  it("flags thin history and poor agreement independently", () => {
    const rec = (assessed: number, agreementPct: number) => () => ({ assessed, agreementPct });
    const warn = (r: ReturnType<typeof rec>) => stewardWarnings({ ...base, level: 3, record: r });
    expect(warn(rec(100, 90)).length).toBeGreaterThan(0);
    expect(warn(rec(19, 100)).length).toBeGreaterThan(0);
    expect(warn(rec(20, 95))).toEqual([]);
  });

  it("is quiet at the thresholds", () => {
    expect(
      stewardWarnings({
        ...base,
        level: 3,
        record: () => ({ assessed: 20, agreementPct: 95 }),
      }),
    ).toEqual([]);
  });
});

describe("confirmations", () => {
  it("asks for level 4 and 5 only, and names the move", () => {
    expect(levelChangeConfirmation(3, 3)).toBeNull();
    expect(levelChangeConfirmation(5, 3)).toBeNull();
    expect(levelChangeConfirmation(3, 4)).toContain("Level 3 -> 4");
  });

  it("names stored overrides when a raise to level 3 wakes them, and only then", () => {
    const o = { priority: "veto" as const, bookkeeping: "human" as const };
    expect(levelChangeConfirmation(1, 3, o)).toContain("Change a ticket's priority (veto)");
    expect(levelChangeConfirmation(1, 3, o)).not.toContain("Blockers");
    expect(levelChangeConfirmation(1, 2, o)).toBeNull();
    expect(levelChangeConfirmation(3, 4, o)).not.toContain("Stored tier overrides");
    expect(levelChangeConfirmation(1, 5, o)).toContain("Level 1 -> 5");
    expect(levelChangeConfirmation(1, 5, o)).toContain("Stored tier overrides");
    expect(levelChangeConfirmation(1, 3, {})).toBeNull();
  });

  it("asks for a judgment category at auto only", () => {
    expect(tierChangeConfirmation(judgment, "auto")).toContain("apply at once");
    expect(tierChangeConfirmation(judgment, "veto")).toBeNull();
    expect(tierChangeConfirmation(STEWARD_CATEGORIES[0], "auto")).toBeNull();
  });
});

describe("the sensitive word list warning", () => {
  const warns = (sensitiveWordsEmpty: boolean | null, overrides = { review_approve: "veto" as const }) =>
    stewardWarnings({ ...base, level: 3, overrides, sensitiveWordsEmpty }).some((w) => w.includes("sensitive word list"));

  it("shows only for an empty list with a judgment category above human", () => {
    expect(warns(true)).toBe(true);
    expect(warns(false)).toBe(false);
    expect(warns(null)).toBe(false);
    expect(warns(true, {} as never)).toBe(false);
  });
});
