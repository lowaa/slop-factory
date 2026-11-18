import { describe, expect, it, vi } from "vitest";
import { apiClient } from "@/api";
import { makeAgent, makeConductor } from "@/test/fixtures";
import { listPickableAgents } from "./pickable_agents";

describe("listPickableAgents", () => {
  it("offers the workers and the person's own conductors, never a disabled one", async () => {
    vi.spyOn(apiClient, "listAgents").mockResolvedValue([
      makeAgent({ id: "a", name: "worker" }),
      makeAgent({ id: "b", name: "off", disabled_at: "2026-01-01T00:00:00Z" }),
    ]);
    vi.spyOn(apiClient, "listConductors").mockResolvedValue({
      own: [makeConductor({ id: "c", name: "mine" }), makeConductor({ id: "d", name: "mine-off", disabled_at: "2026-01-01T00:00:00Z" })],
      others: [makeConductor({ id: "e", name: "theirs", owned_by_me: false })],
    });

    expect(await listPickableAgents()).toEqual([
      { id: "a", name: "worker" },
      { id: "c", name: "mine" },
    ]);
  });

  it("never offers an archived agent, even if the list carried one", async () => {
    // Archived but enabled cannot happen; the filter must not lean on disabled.
    const away = { archived_at: "2026-01-02T00:00:00Z" };
    vi.spyOn(apiClient, "listAgents").mockResolvedValue([makeAgent({ id: "a", name: "worker" }), makeAgent({ id: "b", name: "away", ...away })]);
    vi.spyOn(apiClient, "listConductors").mockResolvedValue({ own: [makeConductor({ id: "c", name: "gone", ...away })], others: [] });

    expect(await listPickableAgents()).toEqual([{ id: "a", name: "worker" }]);
  });

  it("still offers the workers when the conductors cannot be read", async () => {
    vi.spyOn(apiClient, "listAgents").mockResolvedValue([makeAgent({ id: "a", name: "worker" })]);
    vi.spyOn(apiClient, "listConductors").mockRejectedValue(new Error("down"));

    expect(await listPickableAgents()).toEqual([{ id: "a", name: "worker" }]);
  });

  it("still offers the conductors when the workers cannot be read", async () => {
    vi.spyOn(apiClient, "listAgents").mockRejectedValue(new Error("down"));
    vi.spyOn(apiClient, "listConductors").mockResolvedValue({ own: [makeConductor({ id: "c", name: "mine" })], others: [] });

    expect(await listPickableAgents()).toEqual([{ id: "c", name: "mine" }]);
  });
});
