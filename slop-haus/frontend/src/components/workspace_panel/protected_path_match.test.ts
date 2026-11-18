import { describe, expect, it } from "vitest";
import { firstMatchingRule, pathRuleMatches } from "./protected_path_match";

// Cases mirror the server's TestProtected_MatchesLikeSharedPaths.
describe("pathRuleMatches", () => {
  it.each([
    ["infrastructure/", "infrastructure/k8s/a.yaml", true],
    ["infrastructure/", "infrastructure/", true],
    ["infrastructure/", "infrastructure", false],
    ["*.env", "prod.env", true],
    ["*.env", "sub/prod.env", false],
    ["server/erasure*", "server/erasure_job.go", true],
    ["gen/*", "gen/", false],
    ["gen/*", "gen/a.ts", true],
    ["a?c", "abc", true],
    ["a?c", "a/c", false],
    ["[ab].go", "a.go", true],
    ["[^ab].go", "a.go", false],
    ["a.b", "axb", false],
    ["[a", "a", false],
  ])("%s vs %s is %s", (rule, rel, want) => {
    expect(pathRuleMatches(rule, rel)).toBe(want);
  });
});

describe("firstMatchingRule", () => {
  const rules = [{ path: "infra/" }, { path: "" }, { path: "*.env" }];
  it("returns the first covering rule, ignoring blank rows and a leading ./", () => {
    expect(firstMatchingRule(rules, "./infra/a")).toBe(rules[0]);
    expect(firstMatchingRule(rules, "x.env")).toBe(rules[2]);
  });
  it("returns nothing for an empty or uncovered path", () => {
    expect(firstMatchingRule(rules, "  ")).toBeUndefined();
    expect(firstMatchingRule(rules, "src/a.go")).toBeUndefined();
  });
});
