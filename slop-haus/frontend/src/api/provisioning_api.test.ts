// The provisioning half of apiClient's wire behaviour, against a mocked fetch; see index.test.ts
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { installFetch, jsonResponse, wireOf, type FetchMock } from "./fetch_recorder";

type Api = typeof import("./index");

describe("apiClient provisioning", () => {
  let fetchMock: FetchMock;
  let api: Api;

  beforeEach(async () => {
    ({ fetchMock, api } = await installFetch());
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("unwraps the {supervisors: [...]} envelope", async () => {
    fetchMock.mockResolvedValue(jsonResponse(200, { supervisors: [{ id: "s1", name: "box" }] }));

    expect(await api.apiClient.listProvisionSupervisors()).toEqual([{ id: "s1", name: "box" }]);
    expect((await wireOf(fetchMock)).url).toBe("/api/supervisors");
  });

  it("GETs a supervisor's desired state", async () => {
    fetchMock.mockResolvedValue(jsonResponse(200, { revision: 3, agents: [] }));

    expect(await api.apiClient.getDesiredState("s1")).toEqual({ revision: 3, agents: [] });
    expect(await wireOf(fetchMock)).toMatchObject({ url: "/api/supervisors/s1/desired_state", method: "GET" });
  });

  it("PUTs the revision it was based on with the whole agent list", async () => {
    fetchMock.mockResolvedValue(jsonResponse(200, { revision: 4, agents: [] }));
    const agent = { kind: "slot", name: "a", state: "running", resume_generation: 0 } as const;

    await api.apiClient.putDesiredState("s1", 3, [agent]);

    expect(await wireOf(fetchMock)).toEqual({
      url: "/api/supervisors/s1/desired_state",
      method: "PUT",
      body: { revision: 3, agents: [agent] },
    });
  });

  it("throws an ApiError carrying 409 for a stale revision", async () => {
    fetchMock.mockResolvedValue(jsonResponse(409, { title: "Conflict", status: 409, detail: "read it again" }));

    await expect(api.apiClient.putDesiredState("s1", 1, [])).rejects.toMatchObject({ status: 409 });
  });

  it("unwraps the unmanaged slots of a supervisor", async () => {
    const slot = { kind: "slot", name: "x", offline: false, phase: "running" };
    fetchMock.mockResolvedValue(jsonResponse(200, { slots: [slot] }));

    expect(await api.apiClient.listUnmanagedSlots("s1")).toEqual([slot]);
    expect(await wireOf(fetchMock)).toMatchObject({ url: "/api/supervisors/s1/unmanaged_slots", method: "GET" });
  });

  it("POSTs the revision and names to import", async () => {
    fetchMock.mockResolvedValue(jsonResponse(200, { revision: 4, agents: [] }));

    await api.apiClient.importSupervisorSlots("s1", 3, ["x"]);

    expect(await wireOf(fetchMock)).toEqual({
      url: "/api/supervisors/s1/desired_state/import",
      method: "POST",
      body: { revision: 3, names: ["x"] },
    });
  });

  describe("a person's own supervisor-run remote controls", () => {
    const rc = (id: string, name: string) => ({ id, name, kind: "remote_control", state: "running", launch: "supervisor", observed: { generation: 0 } });

    it("reads whether the board provisions and the supervisors the person approved", async () => {
      fetchMock.mockResolvedValue(jsonResponse(200, { provision_from_board: true, supervisors: [{ id: "s1", name: "box", remote_controls: [] }] }));

      expect(await api.apiClient.listOwnRemoteControlSupervisors()).toEqual({
        provisionFromBoard: true,
        supervisors: [{ id: "s1", name: "box", remote_controls: [] }],
      });
      expect(await wireOf(fetchMock)).toMatchObject({ url: "/api/remote_control/supervisors", method: "GET" });
    });

    it("POSTs the name and harness to the chosen supervisor", async () => {
      fetchMock.mockResolvedValue(jsonResponse(201, rc("e1", "mine")));

      await api.apiClient.addOwnRemoteControl("s1", "mine", "pi");

      expect(await wireOf(fetchMock)).toEqual({ url: "/api/remote_control/supervisors/s1/remote_controls", method: "POST", body: { name: "mine", harness: "pi" } });
    });

    it("removes the named entry of the supervisor that runs it, not a same-named one elsewhere", async () => {
      fetchMock
        .mockResolvedValueOnce(
          jsonResponse(200, {
            provision_from_board: true,
            supervisors: [
              { id: "s1", name: "box", remote_controls: [rc("e-other", "mine")] },
              { id: "s2", name: "other", remote_controls: [rc("e-elsewhere", "theirs"), rc("e-right", "mine")] },
            ],
          }),
        )
        .mockResolvedValueOnce(new Response(null, { status: 204 }));

      await api.apiClient.removeOwnRemoteControl("s2", "mine");

      expect(await wireOf(fetchMock, 1)).toMatchObject({ url: "/api/remote_control/supervisors/s2/remote_controls/e-right", method: "DELETE" });
    });

    it("sends no DELETE when the supervisor has no such entry", async () => {
      fetchMock.mockResolvedValueOnce(jsonResponse(200, { provision_from_board: true, supervisors: [{ id: "s1", name: "box", remote_controls: [] }] }));

      await expect(api.apiClient.removeOwnRemoteControl("s1", "mine")).rejects.toThrow("not on a supervisor you approved");
      expect(fetchMock).toHaveBeenCalledTimes(1);
    });
  });

  it("PATCHes the new slot cap", async () => {
    fetchMock.mockResolvedValue(jsonResponse(200, { id: "s1", name: "box", slot_cap: 6, slot_count: 4 }));

    expect(await api.apiClient.setSupervisorSlotCap("s1", 6)).toMatchObject({ slot_cap: 6 });
    expect(await wireOf(fetchMock)).toEqual({ url: "/api/supervisors/s1/slot-cap", method: "PATCH", body: { slot_cap: 6 } });
  });

  it("PATCHes a supervisor disabled", async () => {
    fetchMock.mockResolvedValue(jsonResponse(200, { id: "s1", name: "box", disabled_at: "2026-01-01T00:00:00Z" }));

    expect(await api.apiClient.setSupervisorDisabled("s1", true)).toMatchObject({ id: "s1" });
    expect(await wireOf(fetchMock)).toEqual({ url: "/api/supervisors/s1", method: "PATCH", body: { disabled: true } });
  });
});
