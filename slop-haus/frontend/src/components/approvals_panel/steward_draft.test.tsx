import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient, ApiError, type Proposal } from "@/api";
import StewardDraft from "./steward_draft";

type Draft = NonNullable<Proposal["draft"]>;

const draft: Draft = {
  applied: false,
  body: "Looks good; merging.",
  category: "merge_continue",
  evidence: [
    { check: "ci_green_on_head", ok: true, ref: "run-1" },
    { check: "diff_within_scope", ok: false, detail: "touches other/" },
  ],
  reason: "CI is green",
  revision: 3,
  would_send_back: false,
};

const show = (over: Partial<Draft> = {}, onApplied = vi.fn(), needsConfirmation = false) => {
  render(<StewardDraft proposalId="p1" draft={{ ...draft, ...over }} needsConfirmation={needsConfirmation} onApplied={onApplied} />);
  return onApplied;
};

describe("StewardDraft", () => {
  it("shows category, reason, every check with its ref, and the text", () => {
    show();
    expect(screen.getByText("merge_continue")).toBeInTheDocument();
    expect(screen.getByText("CI is green")).toBeInTheDocument();
    expect(screen.getByText("✓ ci_green_on_head (Ref: run-1)")).toBeInTheDocument();
    expect(screen.getByText("✗ diff_within_scope: touches other/")).toBeInTheDocument();
    expect(screen.getByText("Looks good; merging.")).toBeInTheDocument();
  });

  it("applies the draft on click", async () => {
    const apply = vi.spyOn(apiClient, "applyStewardDraft").mockResolvedValue({} as Proposal);
    const onApplied = show();
    await userEvent.click(screen.getByRole("button", { name: "Apply steward's draft" }));
    expect(apply).toHaveBeenCalledWith("p1");
    await vi.waitFor(() => expect(onApplied).toHaveBeenCalled());
  });

  it("asks once more before applying an irreversible draft", async () => {
    const apply = vi.spyOn(apiClient, "applyStewardDraft").mockResolvedValue({} as Proposal);
    const onApplied = show({}, vi.fn(), true);
    await userEvent.click(screen.getByRole("button", { name: "Apply steward's draft" }));
    expect(apply).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "Confirm: apply steward's draft" }));
    expect(apply).toHaveBeenCalledWith("p1");
    await vi.waitFor(() => expect(onApplied).toHaveBeenCalled());
  });

  it("shows a stale or refused error inline and does not report it applied", async () => {
    vi.spyOn(apiClient, "applyStewardDraft").mockRejectedValue(new ApiError(409, "the draft is out of date"));
    const onApplied = show();
    await userEvent.click(screen.getByRole("button", { name: "Apply steward's draft" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("the draft is out of date");
    expect(onApplied).not.toHaveBeenCalled();
  });

  it("marks review notes as would send back, with no apply button", () => {
    show({ would_send_back: true, body: "Add a test." });
    expect(screen.getByText("would send back")).toBeInTheDocument();
    expect(screen.getByText("Add a test.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Apply/ })).toBeNull();
  });

  it("offers no apply button once applied", () => {
    show({ applied: true });
    expect(screen.queryByRole("button", { name: /Apply/ })).toBeNull();
    expect(screen.getByText("This draft was already applied.")).toBeInTheDocument();
  });
});
