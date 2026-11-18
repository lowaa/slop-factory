import { describe, expect, it } from "vitest";
import { describeRequiredRoles } from "./roles";

describe("describeRequiredRoles", () => {
  it("is empty when there are no required roles", () => {
    expect(describeRequiredRoles([], "any")).toBe("");
  });

  it("names a single role without a match word", () => {
    expect(describeRequiredRoles([{ id: "r1", name: "deploy" }], "all")).toBe("role: deploy");
  });

  it("says 'any of' for two-or-more roles matched any", () => {
    expect(
      describeRequiredRoles(
        [
          { id: "r1", name: "deploy" },
          { id: "r2", name: "review" },
        ],
        "any",
      ),
    ).toBe("roles: any of deploy, review");
  });

  it("says 'all of' for two-or-more roles matched all", () => {
    expect(
      describeRequiredRoles(
        [
          { id: "r1", name: "deploy" },
          { id: "r2", name: "review" },
        ],
        "all",
      ),
    ).toBe("roles: all of deploy, review");
  });
});
