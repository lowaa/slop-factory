import { describe, expect, it } from "vitest";
import { suggestTenantName } from "./tenant_names";

describe("suggestTenantName", () => {
  it("is the display name when it is free", () => {
    expect(suggestTenantName("  Ada ", ["Lab"])).toBe("Ada");
  });

  it("numbers a name the person already has, ignoring case", () => {
    expect(suggestTenantName("Ada", ["ada"])).toBe("Ada 2");
    expect(suggestTenantName("Ada", ["Ada", "ada 2"])).toBe("Ada 3");
  });

  it("keeps within the length limit, counting characters", () => {
    const long = "日".repeat(60);
    expect(Array.from(suggestTenantName(long, [])).length).toBe(40);
    const second = suggestTenantName(long, ["日".repeat(40)]);
    expect(Array.from(second).length).toBeLessThanOrEqual(40);
    expect(second.endsWith(" 2")).toBe(true);
  });
});
