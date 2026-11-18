import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import type { SupervisorStatus } from "@/api";
import SupervisorSlots from "./supervisor_slots";
import { SLOT_REASONS } from "./slot_reasons";

type Slot = SupervisorStatus["slots"][number];

const slot: Slot = {
  agent_id: "alpha",
  agent_name: "alpha",
  error_class: "",
  error_streak: 0,
  offline: false,
  pause_reason: "",
  reason: "unknown",
  reason_detail: "",
  bundle_hash: "",
  bundle_status: "unknown" as const,
  paused_at: null,
  paused_long: false,
  phase: "idle",
  phase_entered_at: "2026-10-19T10:00:00Z",
  reported_at: "2026-10-19T10:09:50Z",
  stall_reason: "",
  stalled: false,
  ticket_id: null,
};
const NOW = Date.parse("2026-10-19T10:10:00Z");

const reasons = Object.keys(SLOT_REASONS) as Slot["reason"][];

describe("SupervisorSlots reasons", () => {
  it.each(reasons.filter((r) => r !== "unknown"))("labels %s in words", (reason) => {
    render(<SupervisorSlots now={NOW} slots={[{ ...slot, reason }]} />);
    expect(screen.getByText(SLOT_REASONS[reason])).toBeTruthy();
    expect(SLOT_REASONS[reason]).not.toContain("_");
  });

  it("says nothing for a supervisor that does not report a reason", () => {
    const { container } = render(<SupervisorSlots now={NOW} slots={[slot]} />);
    expect(container.querySelector(".supervisor-slots__reason")).toBeNull();
  });

  it("says nothing while the slot is running", () => {
    const { container } = render(<SupervisorSlots now={NOW} slots={[{ ...slot, phase: "running", reason: "no_claimable" }]} />);
    expect(container.querySelector(".supervisor-slots__reason")).toBeNull();
  });

  it("marks a slot whose workspace scripts are out of date, and only that one", () => {
    render(
      <SupervisorSlots
        now={NOW}
        slots={[
          { ...slot, agent_id: "old", agent_name: "old", bundle_hash: "aaaa", bundle_status: "stale" },
          { ...slot, agent_id: "new", agent_name: "new", bundle_hash: "bbbb", bundle_status: "current" },
          { ...slot, agent_id: "mute", agent_name: "mute" },
        ]}
      />,
    );
    expect(screen.getAllByText("workspace scripts out of date")).toHaveLength(1);
    expect(screen.getByText("workspace scripts out of date")).toHaveAttribute("title", expect.stringContaining("aaaa"));
  });

  it("shows the detail as text, never markup", () => {
    const { container } = render(
      <SupervisorSlots now={NOW} slots={[{ ...slot, reason: "other", reason_detail: "<b>bold</b>" }]} />,
    );
    expect(container.querySelector(".supervisor-slots__reason b")).toBeNull();
    expect(screen.getByText(/<b>bold<\/b>/)).toBeTruthy();
  });
});
