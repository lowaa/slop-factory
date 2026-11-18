import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Proposal } from "@/api";
import StewardLabel from "./steward_label";

type Steward = NonNullable<Proposal["steward"]>;

const base: Steward = {
  category: "merge_continue",
  tier: "auto",
  verdict: "would_auto",
  mode: "shadow",
  reason: "CI is green",
  evidence: [
    { check: "ci_green_on_head", ok: true, ref: "run-1" },
    { check: "diff_within_scope", ok: false, detail: "touches other/" },
  ],
};

describe("StewardLabel", () => {
  it.each([
    [{ verdict: "would_auto" as const }, "steward: would auto-approve (merge_continue)"],
    [{ verdict: "would_veto" as const, tier: "veto" as const, veto_minutes: 45 }, "steward: would approve after 45 min"],
    [{ verdict: "needs_human" as const, tier: "human" as const }, "steward: needs you"],
  ])("says %j as %s", (over, text) => {
    render(<StewardLabel steward={{ ...base, ...over }} />);
    expect(screen.getByText(text)).toBeInTheDocument();
  });

  it("keeps the reason and evidence collapsed until opened", async () => {
    const { container } = render(<StewardLabel steward={base} />);
    const details = container.querySelector("details")!;
    expect(details.open).toBe(false);
    await userEvent.click(screen.getByText(/would auto-approve/));
    expect(details.open).toBe(true);
    expect(screen.getByText("CI is green")).toBeInTheDocument();
    expect(screen.getByText("✓ ci_green_on_head (run-1)")).toBeInTheDocument();
    expect(screen.getByText("✗ diff_within_scope: touches other/")).toBeInTheDocument();
  });
});
