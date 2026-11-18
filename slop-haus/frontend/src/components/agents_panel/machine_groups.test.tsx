import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, within } from "@testing-library/react";
import { apiClient, type DesiredState } from "@/api";
import { makeAgent, makeTenant } from "@/test/fixtures";
import { renderSignedIn } from "@/test/render_signed_in";
import AgentsPanel from "./agents_panel";
import { stubPanelDefaults, stubSideLists } from "./agents_panel_test_support";

stubPanelDefaults();

const supervisor = { id: "s1", name: "box", archived_at: null, created_at: "", disabled_at: null, slot_cap: 4, slot_count: 2, last_seen_at: "2026-01-01T00:00:00Z", stale: false };
const document: DesiredState = {
  revision: 1,
  agents: [{ id: "d1", kind: "slot", name: "alpha", state: "running", generation: 1, resume_generation: 0, observed: { generation: 1, phase: "running" } }],
};

beforeEach(() => {
  stubSideLists();
  vi.spyOn(apiClient, "getSetting").mockResolvedValue({ name: "provision_from_board", type: "bool", bool_val: true } as never);
  vi.spyOn(apiClient, "listProvisionSupervisors").mockResolvedValue([supervisor]);
  vi.spyOn(apiClient, "getDesiredState").mockResolvedValue(document);
  vi.spyOn(apiClient, "listTenants").mockResolvedValue([makeTenant({ is_owner: false, current: true })]);
});

describe("AgentsPanel grouped by machine", () => {
  it("puts a worker's machine controls on its own row, and leaves removed and unsupervised workers without", async () => {
    vi.spyOn(apiClient, "listAgents").mockResolvedValue([
      makeAgent({ id: "w1", name: "alpha", supervisor_agent_id: "s1" }),
      makeAgent({ id: "w2", name: "loner" }),
      makeAgent({ id: "w3", name: "gone", supervisor_agent_id: "s1", archived_at: "2026-10-01T00:00:00Z", disabled_at: "2026-10-01T00:00:00Z" }),
    ]);
    await renderSignedIn(<AgentsPanel />);
    const group = await screen.findByRole("region", { name: "Workers on box" });
    const row = (await within(group).findByRole("button", { name: "Pause alpha" })).closest("li") as HTMLElement;
    expect(row).toHaveTextContent("alpha");
    const loner = within(screen.getByRole("region", { name: "Not on a supervisor" })).getByText("loner").closest("li") as HTMLElement;
    expect(within(loner).queryByRole("button", { name: /Pause|Retire/ })).toBeNull();
    expect(screen.getAllByRole("link", { name: "Add a machine" })).toHaveLength(1);
  });

  it("keeps a filtered-out worker's name off the waiting list", async () => {
    vi.spyOn(apiClient, "listAgents").mockResolvedValue([makeAgent({ id: "w1", name: "alpha", supervisor_agent_id: "s1", disabled_at: "2026-10-01T00:00:00Z" })]);
    await renderSignedIn(<AgentsPanel />);
    await screen.findByRole("region", { name: "Workers on box" });
    await (await import("@testing-library/user-event")).default.click(screen.getByLabelText("Hide disabled"));
    await screen.findByText("Every worker is disabled.");
    expect(screen.queryByRole("list", { name: "Waiting on box" })).toBeNull();
  });
});
