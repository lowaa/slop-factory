import { describe, expect, it, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import { apiClient, type SupervisorStatus } from "@/api";
import { renderSignedIn } from "@/test/render_signed_in";
import PausedSlotsNotice from "./paused_slots_notice";

type Slot = SupervisorStatus["slots"][number];

const paused = (overrides: Partial<Slot> = {}): Slot => ({
  agent_id: "alpha",
  agent_name: "alpha",
  error_class: "",
  error_streak: 0,
  offline: false,
  pause_reason: "git push refused",
  reason: "unknown" as const,
  reason_detail: "",
  bundle_hash: "",
  bundle_status: "unknown" as const,
  paused_at: "2026-10-19T10:00:00Z",
  paused_long: true,
  phase: "paused",
  phase_entered_at: "2026-10-19T10:00:00Z",
  reported_at: "2026-10-19T10:20:00Z",
  stall_reason: "",
  stalled: false,
  ticket_id: null,
  ...overrides,
});

const serve = (slots: Slot[]) =>
  vi.spyOn(apiClient, "getSupervisorStatus").mockResolvedValue({ offline: false, claimable: 1, last_polled_at: null, slots, supervisors: [], current_bundle_hash: "", current_protocol: 0 });

describe("PausedSlotsNotice", () => {
  it("says so once a slot has been paused past the server's bound, and links to the Workers page", async () => {
    serve([paused()]);

    await renderSignedIn(<PausedSlotsNotice refreshToken={0} />);

    const notice = await screen.findByRole("status", { name: "Paused slots" });
    expect(notice).toHaveTextContent("1 supervisor slot is paused and not taking work");
    expect(screen.getByRole("link", { name: "Workers page" })).toHaveAttribute("href", "/workers");
  });

  it("stays quiet for a slot paused for less than the bound", async () => {
    const status = serve([paused({ paused_long: false })]);

    await renderSignedIn(<PausedSlotsNotice refreshToken={0} />);

    await waitFor(() => expect(status).toHaveBeenCalled());
    expect(screen.queryByRole("status", { name: "Paused slots" })).not.toBeInTheDocument();
  });

  it("counts only the long-paused slots", async () => {
    serve([paused(), paused({ agent_id: "b" }), paused({ agent_id: "c", paused_long: false })]);

    await renderSignedIn(<PausedSlotsNotice refreshToken={0} />);

    expect(await screen.findByRole("status", { name: "Paused slots" })).toHaveTextContent("2 supervisor slots are paused");
  });

  it("shows nothing when the status cannot be read", async () => {
    const status = vi.spyOn(apiClient, "getSupervisorStatus").mockRejectedValue(new Error("boom"));

    await renderSignedIn(<PausedSlotsNotice refreshToken={0} />);

    await waitFor(() => expect(status).toHaveBeenCalled());
    expect(screen.queryByRole("status", { name: "Paused slots" })).not.toBeInTheDocument();
  });
});
