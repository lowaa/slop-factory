import { client, unwrap } from "./client";
import type { components } from "@/api_generated/schema";

type Schemas = components["schemas"];

// The desired-state document of one supervisor, with what the supervisor
// has reported about each agent.
export type DesiredState = Schemas["DesiredStateBody"];
export type DesiredAgentView = Schemas["DesiredAgentView"];
// What a write names for each agent; no observed half.
export type DesiredAgent = Schemas["DesiredAgentBody"];
// A slot the supervisor reports that its document does not name.
export type UnmanagedSlot = Schemas["UnmanagedSlotBody"];
export type ProvisionSupervisor = Schemas["SupervisorBody"];
// A supervisor the signed-in person approved, with the Remote Controls it runs.
export type OwnRemoteControlSupervisor = Schemas["OwnRemoteControlSupervisorBody"];
export interface OwnRemoteControlSupervisors {
  provisionFromBoard: boolean;
  supervisors: OwnRemoteControlSupervisor[];
}

export const provisioningApi = {
  async listProvisionSupervisors(): Promise<ProvisionSupervisor[]> {
    return (await unwrap(client.GET("/api/supervisors"))).supervisors;
  },

  async setSupervisorDisabled(supervisorId: string, disabled: boolean): Promise<ProvisionSupervisor> {
    return await unwrap(
      client.PATCH("/api/supervisors/{id}", { params: { path: { id: supervisorId } }, body: { disabled } }),
    );
  },

  async getDesiredState(supervisorId: string): Promise<DesiredState> {
    return await unwrap(
      client.GET("/api/supervisors/{id}/desired_state", { params: { path: { id: supervisorId } } }),
    );
  },

  // Replaces the whole document. revision is the one the caller read: a
  // stale one is refused with a 409 ApiError, and the caller reads again.
  async putDesiredState(
    supervisorId: string,
    revision: number,
    agents: DesiredAgent[],
  ): Promise<DesiredState> {
    return await unwrap(
      client.PUT("/api/supervisors/{id}/desired_state", {
        params: { path: { id: supervisorId } },
        body: { revision, agents },
      }),
    );
  },

  // -- A person's own supervisor-run Remote Controls, from the Remote Control page --

  async listOwnRemoteControlSupervisors(): Promise<OwnRemoteControlSupervisors> {
    const body = await unwrap(client.GET("/api/remote_control/supervisors"));
    return { provisionFromBoard: body.provision_from_board, supervisors: body.supervisors };
  },

  // addOwnRemoteControl has a supervisor the person approved make a Remote
  // Control for them and run it. A stale sign-in is a 403 reauth_required.
  async addOwnRemoteControl(supervisorId: string, name: string, harness?: string): Promise<DesiredAgentView> {
    return await unwrap(
      client.POST("/api/remote_control/supervisors/{id}/remote_controls", {
        params: { path: { id: supervisorId } },
        body: { name, harness },
      }),
    );
  },

  // removeOwnRemoteControl takes the named Remote Control off the supervisor
  // that runs it, which retires its conductor.
  async removeOwnRemoteControl(supervisorId: string, name: string): Promise<void> {
    const { supervisors } = await provisioningApi.listOwnRemoteControlSupervisors();
    const entry = supervisors.find((s) => s.id === supervisorId)?.remote_controls.find((r) => r.name === name);
    if (!entry?.id) throw new Error(`${name} is not on a supervisor you approved`);
    await unwrap(
      client.DELETE("/api/remote_control/supervisors/{id}/remote_controls/{entry_id}", {
        params: { path: { id: supervisorId, entry_id: entry.id } },
      }),
    );
  },

  async listUnmanagedSlots(supervisorId: string): Promise<UnmanagedSlot[]> {
    return (
      await unwrap(
        client.GET("/api/supervisors/{id}/unmanaged_slots", { params: { path: { id: supervisorId } } }),
      )
    ).slots;
  },

  // Adds the named reported slots to the document. revision is the one the
  // caller read: a stale one is refused with a 409 ApiError.
  async importSupervisorSlots(supervisorId: string, revision: number, names: string[]): Promise<DesiredState> {
    return await unwrap(
      client.POST("/api/supervisors/{id}/desired_state/import", {
        params: { path: { id: supervisorId } },
        body: { revision, names },
      }),
    );
  },

  // Sets how many live workers a supervisor may have. The tenant's owner only;
  // a 422 ApiError for a cap the server refuses.
  async setSupervisorSlotCap(supervisorId: string, slotCap: number): Promise<ProvisionSupervisor> {
    return await unwrap(
      client.PATCH("/api/supervisors/{id}/slot-cap", {
        params: { path: { id: supervisorId } },
        body: { slot_cap: slotCap },
      }),
    );
  },
};
