import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import { apiClient, type SupervisorStatus } from "@/api";
import { renderSignedIn } from "@/test/render_signed_in";
import SupervisorsPage from "./supervisors_page";

const slot: SupervisorStatus["slots"][number] = {
  agent_id: "alpha",
  agent_name: "alpha",
  error_class: "",
  error_streak: 0,
  offline: false,
  pause_reason: "",
  reason: "unknown" as const,
  reason_detail: "",
  bundle_hash: "",
  bundle_status: "unknown" as const,
  paused_at: null,
  paused_long: false,
  phase: "running",
  phase_entered_at: "2026-10-19T10:00:00Z",
  reported_at: "2026-10-19T10:09:50Z",
  stall_reason: "",
  stalled: false,
  ticket_id: 42,
};

function stubStatus(status: SupervisorStatus) {
  vi.spyOn(apiClient, "getSupervisorStatus").mockResolvedValue(status);
}

beforeEach(() => {
  vi.restoreAllMocks();
});

describe("SupervisorsPage", () => {
  it("links the setup script download to the route", async () => {
    stubStatus({ offline: false, claimable: 0, last_polled_at: null, slots: [], supervisors: [], current_bundle_hash: "", current_protocol: 0 });
    await renderSignedIn(<SupervisorsPage />);
    const link = screen.getByRole("link", { name: "Download setup script" });
    expect(link.getAttribute("href")).toBe("/api/supervisor/setup_script");
    expect(link.getAttribute("download")).toBe("slop-haus-supervisor-setup.sh");
  });

  it("shows the setup, setup script and start commands, each with a copy button, and the day-to-day ones", async () => {
    stubStatus({ offline: false, claimable: 0, last_polled_at: null, slots: [], supervisors: [], current_bundle_hash: "", current_protocol: 0 });
    await renderSignedIn(<SupervisorsPage />);
    expect(screen.getByText("bash slop-haus-supervisor-setup.sh")).toBeTruthy();
    expect(screen.getByText("slop-haus start")).toBeTruthy();
    expect(screen.getByRole("button", { name: /the setup script command/i })).toBeTruthy();
    expect(screen.getByRole("button", { name: /the setup command/i })).toBeTruthy();
    expect(screen.getByRole("button", { name: /the start command/i })).toBeTruthy();
    const order = Array.from(document.querySelectorAll(".supervisors-page__section .command pre")).map((el) => el.textContent);
    expect(order).toEqual(["bash slop-haus-supervisor-setup.sh", "slop-haus setup", "slop-haus start"]);
    expect(document.body).toHaveTextContent(/slop-haus status.*slop-haus logs.*slop-haus stop/);
  });

  it("ends with a collapsed Add a slot manually section", async () => {
    stubStatus({ offline: false, claimable: 0, last_polled_at: null, slots: [], supervisors: [], current_bundle_hash: "", current_protocol: 0 });
    await renderSignedIn(<SupervisorsPage />);
    const sections = document.querySelectorAll<HTMLDetailsElement>("details");
    const manual = sections[sections.length - 1];
    expect(manual.querySelector("summary")).toHaveTextContent("Add a slot manually");
    expect(manual.open).toBe(false);
    expect(manual.nextElementSibling).toBeNull();
  });

  it("names SSH or HTTPS and claude, pi or codex in the requirements", async () => {
    stubStatus({ offline: false, claimable: 0, last_polled_at: null, slots: [], supervisors: [], current_bundle_hash: "", current_protocol: 0 });
    await renderSignedIn(<SupervisorsPage />);
    expect(document.body).toHaveTextContent("claude, pi or codex");
    expect(document.body).toHaveTextContent("push over SSH or HTTPS");
  });

  it("no longer lists supervisors or their workers: that is the Workers page's job", async () => {
    stubStatus({
      offline: true,
      claimable: 2,
      last_polled_at: "2026-10-19T10:00:00Z",
      slots: [slot],
      supervisors: [{ agent_id: "s1", agent_name: "old-box", version: "v1", protocol: 1, protocol_status: "stale", last_polled_at: "2026-10-19T10:00:00Z" }],
      current_bundle_hash: "",
      current_protocol: 2,
    });
    const supervisors = vi.spyOn(apiClient, "listProvisionSupervisors");
    await renderSignedIn(<SupervisorsPage />);
    expect(await screen.findByRole("link", { name: "Workers page" })).toHaveAttribute("href", "/workers");
    expect(supervisors).not.toHaveBeenCalled();
    expect(screen.queryByText("Agents on each supervisor")).toBeNull();
    expect(screen.queryByText("alpha")).toBeNull();
    expect(screen.queryByText(/Supervisor offline/)).toBeNull();
  });

  it("links back to Workers once a slot has reported, even with no poll yet", async () => {
    stubStatus({ offline: false, claimable: 0, last_polled_at: null, slots: [slot], supervisors: [], current_bundle_hash: "", current_protocol: 0 });
    await renderSignedIn(<SupervisorsPage />);
    expect(await screen.findByRole("link", { name: "Workers page" })).toBeTruthy();
  });

  it("links back to Workers only once a supervisor has connected", async () => {
    stubStatus({ offline: false, claimable: 0, last_polled_at: null, slots: [], supervisors: [], current_bundle_hash: "", current_protocol: 0 });
    await renderSignedIn(<SupervisorsPage />);
    await screen.findByRole("link", { name: "Download setup script" });
    await waitFor(() => expect(apiClient.getSupervisorStatus).toHaveBeenCalled());
    expect(screen.queryByRole("link", { name: "Workers page" })).toBeNull();
  });

  it("keeps the setup steps when the status request fails", async () => {
    const failed = vi.spyOn(apiClient, "getSupervisorStatus").mockRejectedValue(new Error("down"));
    await renderSignedIn(<SupervisorsPage />);
    await waitFor(() => expect(failed).toHaveBeenCalled());
    expect(screen.getByRole("link", { name: "Download setup script" })).toBeTruthy();
    expect(screen.queryByRole("link", { name: "Workers page" })).toBeNull();
  });
});
