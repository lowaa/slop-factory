import { describe, expect, it } from "vitest";
import type { CompareSummary } from "@/api";
import { blockedReason, buildRequest, formatValue, isBatchId, spreadText, type ComparePick } from "./compare_model";

const BATCH = "123e4567-e89b-12d3-a456-426614174000";
const MAX = 50;

function pick(over: Partial<ComparePick> = {}): ComparePick {
  return {
    source: "filter",
    batchId: "",
    ticketIds: [1, 2],
    ticketsTruncated: false,
    minLines: "",
    maxLines: "",
    baseline: "",
    challengers: ["b"],
    ...over,
  };
}

describe("isBatchId", () => {
  it.each([
    [BATCH, true],
    [BATCH.toUpperCase(), true],
    [`  ${BATCH}\n`, true],
    ["", false],
    ["not-a-uuid", false],
    [BATCH.slice(1), false],
    [`${BATCH}0`, false],
    [BATCH.replace("e", "g"), false],
  ])("%j -> %s", (text, want) => {
    expect(isBatchId(text)).toBe(want);
  });
});

describe("blockedReason", () => {
  it("is null for a valid pick from either source", () => {
    expect(blockedReason(pick(), MAX)).toBeNull();
    expect(blockedReason(pick({ source: "batch", batchId: BATCH, ticketIds: [] }), MAX)).toBeNull();
  });

  it.each([
    ["bad batch id", pick({ source: "batch", batchId: "nope" }), "Enter a replay batch id."],
    ["empty filter", pick({ ticketIds: [] }), "No tickets match the filter."],
    ["truncated", pick({ ticketsTruncated: true }), `More than ${MAX} tickets match; a comparison reads at most ${MAX}. Narrow the filter.`],
    [
      "over the max",
      pick({ ticketIds: Array.from({ length: MAX + 1 }, (_, i) => i) }),
      `More than ${MAX} tickets match; a comparison reads at most ${MAX}. Narrow the filter.`,
    ],
    ["non-numeric min", pick({ minLines: "abc" }), "The smallest size must be a whole number of lines."],
    ["fractional min", pick({ minLines: "1.5" }), "The smallest size must be a whole number of lines."],
    ["negative min", pick({ minLines: "-1" }), "The smallest size must be a whole number of lines."],
    ["negative max", pick({ maxLines: "-3" }), "The largest size must be a whole number of lines."],
    ["bad max", pick({ maxLines: "x" }), "The largest size must be a whole number of lines."],
    ["no challengers", pick({ challengers: [] }), "Pick at least one challenger."],
    ["baseline is a challenger", pick({ baseline: "b", challengers: ["a", "b"] }), "The baseline cannot also be a challenger."],
  ])("blocks on %s", (_name, p, want) => {
    expect(blockedReason(p, MAX)).toBe(want);
  });

  it("accepts exactly maxTickets and blank or zero line bounds", () => {
    expect(blockedReason(pick({ ticketIds: Array.from({ length: MAX }, (_, i) => i), minLines: " ", maxLines: "0" }), MAX)).toBeNull();
  });

  it("reports the first failing rule when several apply", () => {
    expect(blockedReason(pick({ ticketIds: [], minLines: "x", challengers: [] }), MAX)).toBe("No tickets match the filter.");
    expect(blockedReason(pick({ minLines: "x", maxLines: "y", challengers: [] }), MAX)).toBe("The smallest size must be a whole number of lines.");
  });

  it("ignores the other source's fields", () => {
    expect(blockedReason(pick({ source: "batch", batchId: BATCH, ticketIds: [], ticketsTruncated: true }), MAX)).toBeNull();
    expect(blockedReason(pick({ source: "filter", batchId: "garbage" }), MAX)).toBeNull();
  });

  it("allows the default variant as baseline with named challengers", () => {
    expect(blockedReason(pick({ baseline: "", challengers: ["x"] }), MAX)).toBeNull();
    expect(blockedReason(pick({ baseline: "", challengers: [""] }), MAX)).toBe("The baseline cannot also be a challenger.");
  });
});

describe("buildRequest", () => {
  it("sends the trimmed batch id and no ticket ids for a replay batch", () => {
    const req = buildRequest(pick({ source: "batch", batchId: ` ${BATCH} `, ticketIds: [9] }));
    expect(req).toStrictEqual({ replay_batch_id: BATCH, baseline: "", challengers: ["b"] });
  });

  it("sends the ticket ids and no batch id for a filter", () => {
    const req = buildRequest(pick({ batchId: BATCH }));
    expect(req).toStrictEqual({ ticket_ids: [1, 2], baseline: "", challengers: ["b"] });
  });

  it("includes line bounds only when valid, keeping zero", () => {
    expect(buildRequest(pick({ minLines: "0", maxLines: " 40 " }))).toMatchObject({ min_diff_lines: 0, max_diff_lines: 40 });
    const req = buildRequest(pick({ minLines: "", maxLines: "1.5" }));
    expect(req).not.toHaveProperty("min_diff_lines");
    expect(req).not.toHaveProperty("max_diff_lines");
  });
});

describe("formatValue", () => {
  it.each([
    [0, "", false, "0"],
    [0, "lines", true, "0 lines"],
    [5, "", true, "+5"],
    [-5, "", true, "-5"],
    [5, "", false, "5"],
    [1234567, "tokens", false, "1234567 tokens"],
    [0.5, "", false, "0.500"],
    [-0.12345, "$", true, "-0.123 $"],
    [0.0004, "", true, "+0.000"],
    [12.3456, "", false, "12.35"],
    [-1.5, "", true, "-1.50"],
    [1e21, "", false, "1e+21"],
  ])("formatValue(%s, %j, %s) = %j", (v, unit, signed, want) => {
    expect(formatValue(v, unit, signed)).toBe(want);
  });

  it("renders NaN and infinities as text rather than throwing", () => {
    expect(formatValue(NaN, "")).toBe("NaN");
    expect(formatValue(Infinity, "", true)).toBe("+Infinity");
  });
});

describe("spreadText", () => {
  const s = (over: Partial<CompareSummary>): CompareSummary => ({ q1: 0, q3: 0, min: 0, max: 0, median: 0, ...over }) as CompareSummary;

  it("signs both quartiles and both extremes", () => {
    expect(spreadText(s({ q1: -1, q3: 2.5, min: -4, max: 10 }), "min")).toBe("IQR -1 min to +2.50 min; range -4 min to +10 min");
  });

  it("shows a zero spread unsigned", () => {
    expect(spreadText(s({}), "")).toBe("IQR 0 to 0; range 0 to 0");
  });
});
