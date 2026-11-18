import { describe, expect, it } from "vitest";
import { makeProposal } from "@/test/fixtures";
import { isAutoApproved } from "./proposals";

describe("isAutoApproved", () => {
  it("is true only for the server's auto-approved outcome", () => {
    expect(isAutoApproved(makeProposal({ outcome: "auto-approved" }))).toBe(true);
    expect(isAutoApproved(makeProposal({ outcome: "steward-approved" }))).toBe(true);
    expect(isAutoApproved(makeProposal({ outcome: "" }))).toBe(false);
    expect(isAutoApproved(makeProposal({ outcome: "approved by a person" }))).toBe(false);
    expect(isAutoApproved(makeProposal({ outcome: "Auto-approved" }))).toBe(false);
  });
});
