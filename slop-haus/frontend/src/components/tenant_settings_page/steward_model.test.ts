import { describe, expect, it } from "vitest";
import { makeAgent, makeHarnessConfig, makeHarnessConfigScope } from "@/test/fixtures";
import { describeStewardModel, stewardModel } from "./steward_model";

const steward = makeAgent({ id: "s1", kind: "steward" });
const defaults = makeHarnessConfigScope({ settings: { claude: { model: "sonnet", effort: "medium" } } });

describe("stewardModel", () => {
  it("follows the tenant defaults until the steward has its own", () => {
    const m = stewardModel(makeHarnessConfig({ defaults }), [steward]);
    expect(m).toEqual({ model: "sonnet", effort: "medium", source: "default" });
    expect(describeStewardModel(m)).toBe("Model: sonnet, effort medium (tenant default)");
  });

  it("lets the steward's own setting win, setting by setting", () => {
    const own = makeHarnessConfigScope({ agent_id: "s1", settings: { claude: { model: "opus" } } });
    const m = stewardModel(makeHarnessConfig({ defaults, overrides: [own] }), [steward]);
    expect(m).toEqual({ model: "opus", effort: "medium", source: "own" });
    expect(describeStewardModel(m)).toBe("Model: opus, effort medium (steward's own)");
  });

  it("ignores another agent's override", () => {
    const other = makeHarnessConfigScope({ agent_id: "w1", settings: { claude: { model: "opus" } } });
    expect(stewardModel(makeHarnessConfig({ defaults, overrides: [other] }), [steward]).model).toBe("sonnet");
  });

  it("says Claude Code's default when nothing is set", () => {
    expect(describeStewardModel(stewardModel(makeHarnessConfig(), [steward]))).toBe(
      "Model: Claude Code's default (tenant default)",
    );
  });
});
