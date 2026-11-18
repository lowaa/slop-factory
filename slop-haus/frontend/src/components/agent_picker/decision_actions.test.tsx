import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { DecisionActions } from "./decision_actions";

describe("DecisionActions", () => {
  // A bare <button> here once rendered in the browser's default grey:
  // auth-layout__submit only sets its width, and the look comes from the
  // ui Button's variant class.
  it("renders Approve as the primary action and Deny as a secondary one", () => {
    render(<DecisionActions canApprove submitting={null} onDecide={() => {}} />);

    expect(screen.getByRole("button", { name: "Approve" })).toHaveClass("button--primary");
    expect(screen.getByRole("button", { name: "Deny" })).toHaveClass("button--secondary");
  });

  it("holds Approve back until an agent is chosen, but always allows Deny", async () => {
    const onDecide = vi.fn();
    render(<DecisionActions canApprove={false} submitting={null} onDecide={onDecide} />);

    expect(screen.getByRole("button", { name: "Approve" })).toBeDisabled();
    await userEvent.setup().click(screen.getByRole("button", { name: "Deny" }));
    expect(onDecide).toHaveBeenCalledWith("deny");
  });

  it("disables both while an approval is in flight", () => {
    render(<DecisionActions canApprove submitting="approve" onDecide={() => {}} />);

    expect(screen.getByRole("button", { name: "Approving…" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Deny" })).toBeDisabled();
  });

  it("disables both while a denial is in flight", () => {
    render(<DecisionActions canApprove submitting="deny" onDecide={() => {}} />);

    expect(screen.getByRole("button", { name: "Approve" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Denying…" })).toBeDisabled();
  });
});
