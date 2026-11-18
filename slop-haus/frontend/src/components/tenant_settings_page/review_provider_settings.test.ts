import { describe, expect, it } from "vitest";
import {
  EMPTY_CHOICE,
  EMPTY_DRAFT,
  parseStored,
  serializeDraft,
  validateDraft,
  type ChoiceDraft,
  type ProviderDraft,
} from "./review_provider_settings";

const claude: ChoiceDraft = { provider: "claude", model: "", effort: "" };
const draft = (over: Partial<ProviderDraft> = {}): ProviderDraft => ({ primary: claude, fallback: null, fallbackOn: [], ...over });

describe("validateDraft", () => {
  it("accepts a primary alone and a primary with a distinct fallback and a trigger", () => {
    expect(validateDraft(draft())).toEqual([]);
    expect(
      validateDraft(draft({ fallback: { provider: "openai", model: "gpt-5.6-luna", effort: "" }, fallbackOn: ["usage_limit"] })),
    ).toEqual([]);
  });

  it("wants a provider", () => {
    expect(validateDraft(EMPTY_DRAFT)).toHaveLength(1);
    expect(validateDraft(draft({ primary: { ...EMPTY_CHOICE, provider: "nope" } }))).toHaveLength(1);
  });

  it.each([
    ["a slug", "openai/gpt-5.6-luna-pro", true],
    ["a free variant", "vendor/model:free", true],
    ["128 bytes", "a".repeat(128), true],
    ["129 bytes", "a".repeat(129), false],
    ["a double slash", "a//b", false],
    ["a double dot", "a..b", false],
    ["a leading dash", "-a", false],
    ["a space", "a b", false],
  ])("treats %s as a model that is ok: %s", (_name, model, ok) => {
    expect(validateDraft(draft({ primary: { ...claude, model } })).length === 0).toBe(ok);
  });

  it("refuses an effort the provider does not take", () => {
    expect(validateDraft(draft({ primary: { ...claude, effort: "none" } }))).toHaveLength(1);
    expect(validateDraft(draft({ primary: { ...claude, effort: "max" } }))).toEqual([]);
    expect(validateDraft(draft({ primary: { provider: "openai", model: "", effort: "low" } }))).toHaveLength(1);
    expect(validateDraft(draft({ primary: { provider: "codex", model: "", effort: "none" } }))).toEqual([]);
  });

  it("refuses a fallback that repeats the primary or has no trigger", () => {
    expect(validateDraft(draft({ fallback: claude, fallbackOn: ["auth"] }))).toHaveLength(1);
    expect(validateDraft(draft({ fallback: { ...claude, provider: "codex" }, fallbackOn: [] }))).toHaveLength(1);
  });
});

describe("serializeDraft and parseStored", () => {
  it("round-trips, trimming the model and omitting fallback_on without a fallback", () => {
    expect(serializeDraft(draft({ primary: { ...claude, model: " m " } }))).toBe(
      '{"primary":{"provider":"claude","model":"m","reasoning_effort":""}}',
    );
    const full = draft({ fallback: { provider: "codex", model: "", effort: "low" }, fallbackOn: ["auth", "backend"] });
    expect(parseStored(serializeDraft(full))).toEqual({ draft: full, set: true, unreadable: false });
  });

  it("reads blank as none set and anything else unreadable as unreadable", () => {
    expect(parseStored(null)).toMatchObject({ set: false });
    expect(parseStored("  ")).toMatchObject({ set: false });
    expect(parseStored("not json")).toMatchObject({ set: true, unreadable: true });
    expect(parseStored('"text"')).toMatchObject({ set: true, unreadable: true });
  });
});
