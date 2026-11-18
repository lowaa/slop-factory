import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import AddAttemptForm from "./add_attempt_form";
import type { AddAttemptsActions } from "./use_add_attempts";
import type { VariantOptions } from "./use_variant_picker";

const variants = { options: [], loadFailed: false } as unknown as VariantOptions;

function formState(overrides: Partial<AddAttemptsActions> = {}): AddAttemptsActions {
  return {
    rows: [{ key: 0, versionId: "" }],
    capacity: { live: 0, remaining: 5, promoted: null },
    confirming: false,
    replay: false,
    confirm: vi.fn(),
    cancelConfirm: vi.fn(),
    submit: { busy: false, error: null, run: vi.fn() } as unknown as AddAttemptsActions["submit"],
    unavailable: false,
    setVersion: vi.fn(),
    addRow: vi.fn(),
    removeRow: vi.fn(),
    handleSubmit: vi.fn(),
    ...overrides,
  };
}

describe("AddAttemptForm", () => {
  it("warns about side effects when the rows are replays of a done ticket", () => {
    render(<AddAttemptForm variants={variants} form={formState({ replay: true })} />);

    expect(screen.getByRole("heading", { name: "Replay this ticket" })).toBeInTheDocument();
    expect(screen.getByRole("note")).toHaveTextContent(/deploys, pushes to main or touches live systems/);
  });

  it("is the plain add-attempts form, with no warning, for a ticket that is not done", () => {
    render(<AddAttemptForm variants={variants} form={formState()} />);

    expect(screen.getByRole("heading", { name: "Add attempts" })).toBeInTheDocument();
    expect(screen.queryByRole("note")).not.toBeInTheDocument();
  });
});
