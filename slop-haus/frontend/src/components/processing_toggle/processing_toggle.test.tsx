import { describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient, ApiError, type Setting } from "@/api";
import ProcessingToggle from "./processing_toggle";

function makeSetting(overrides: Partial<Setting> = {}): Setting {
  return {
    name: "ticket_processing_enabled",
    type: "bool",
    string_val: null,
    int_val: null,
    float_val: null,
    bool_val: true,
    is_default: true,
    updated_at: null,
    ...overrides,
  };
}

describe("ProcessingToggle", () => {
  it("shows the fleet as processing when the setting is on", async () => {
    vi.spyOn(apiClient, "getSetting").mockResolvedValue(makeSetting({ bool_val: true }));

    render(<ProcessingToggle />);

    const toggle = await screen.findByRole("switch", { name: "Ticket processing" });
    expect(toggle).toBeChecked();
    expect(toggle).toHaveTextContent("Processing");
  });

  // The difference this control exists to communicate: an idle fleet and a
  // paused one look identical on every other screen.
  it("shows the fleet as paused, and says what paused means, when the setting is off", async () => {
    vi.spyOn(apiClient, "getSetting").mockResolvedValue(makeSetting({ bool_val: false, is_default: false }));

    render(<ProcessingToggle />);

    const toggle = await screen.findByRole("switch", { name: "Ticket processing" });
    expect(toggle).not.toBeChecked();
    expect(toggle).toHaveTextContent("Paused");
    // Pausing stops new work starting; it does not abandon a held ticket.
    expect(screen.getByText(/finish current work/i)).toBeInTheDocument();
  });

  it("writes the opposite value when clicked", async () => {
    const user = userEvent.setup();
    vi.spyOn(apiClient, "getSetting").mockResolvedValue(makeSetting({ bool_val: true }));
    const update = vi
      .spyOn(apiClient, "updateSetting")
      .mockResolvedValue(makeSetting({ bool_val: false, is_default: false }));

    render(<ProcessingToggle />);
    await user.click(await screen.findByRole("switch", { name: "Ticket processing" }));

    expect(update).toHaveBeenCalledWith("ticket_processing_enabled", { type: "bool", bool_val: false });
    await waitFor(() => {
      expect(screen.getByRole("switch", { name: "Ticket processing" })).not.toBeChecked();
    });
  });

  it("turns processing back on from the paused state", async () => {
    const user = userEvent.setup();
    vi.spyOn(apiClient, "getSetting").mockResolvedValue(makeSetting({ bool_val: false, is_default: false }));
    const update = vi
      .spyOn(apiClient, "updateSetting")
      .mockResolvedValue(makeSetting({ bool_val: true, is_default: false }));

    render(<ProcessingToggle />);
    await user.click(await screen.findByRole("switch", { name: "Ticket processing" }));

    expect(update).toHaveBeenCalledWith("ticket_processing_enabled", { type: "bool", bool_val: true });
    await waitFor(() => {
      expect(screen.getByRole("switch", { name: "Ticket processing" })).toBeChecked();
    });
  });

  // The server's answer, not the value we asked for: a write that was
  // accepted but landed differently should show what is actually stored.
  it("shows what the server stored rather than what was requested", async () => {
    const user = userEvent.setup();
    vi.spyOn(apiClient, "getSetting").mockResolvedValue(makeSetting({ bool_val: true }));
    vi.spyOn(apiClient, "updateSetting").mockResolvedValue(makeSetting({ bool_val: true, is_default: false }));

    render(<ProcessingToggle />);
    await user.click(await screen.findByRole("switch", { name: "Ticket processing" }));

    await waitFor(() => {
      expect(screen.getByRole("switch", { name: "Ticket processing" })).toBeChecked();
    });
  });

  // The switch reports a failed write next to itself and reverts to
  // whatever it was showing before, rather than assuming why it failed.
  it("explains a rejected write and leaves the switch where it was", async () => {
    const user = userEvent.setup();
    vi.spyOn(apiClient, "getSetting").mockResolvedValue(makeSetting({ bool_val: true }));
    vi.spyOn(apiClient, "updateSetting").mockRejectedValue(new ApiError(500, "internal server error"));

    render(<ProcessingToggle />);
    await user.click(await screen.findByRole("switch", { name: "Ticket processing" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("internal server error");
    expect(screen.getByRole("switch", { name: "Ticket processing" })).toBeChecked();
  });

  it("reports a failure to read the switch at all", async () => {
    vi.spyOn(apiClient, "getSetting").mockRejectedValue(new ApiError(503, "service unavailable"));

    render(<ProcessingToggle />);

    expect(await screen.findByRole("alert")).toHaveTextContent("service unavailable");
  });

  // Two clicks in flight would race, and the loser would write back the
  // value the human just moved away from.
  it("refuses a second click while a write is in flight", async () => {
    const user = userEvent.setup();
    vi.spyOn(apiClient, "getSetting").mockResolvedValue(makeSetting({ bool_val: true }));

    let release: (value: Setting) => void = () => {};
    const pending = new Promise<Setting>((resolve) => {
      release = resolve;
    });
    const update = vi.spyOn(apiClient, "updateSetting").mockReturnValue(pending);

    render(<ProcessingToggle />);
    const toggle = await screen.findByRole("switch", { name: "Ticket processing" });

    await user.click(toggle);
    expect(toggle).toBeDisabled();
    await user.click(toggle);
    expect(update).toHaveBeenCalledTimes(1);

    release(makeSetting({ bool_val: false, is_default: false }));
    await waitFor(() => {
      expect(screen.getByRole("switch", { name: "Ticket processing" })).not.toBeChecked();
    });
  });
});
