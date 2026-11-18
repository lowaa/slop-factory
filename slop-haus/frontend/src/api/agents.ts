import {
  setupScriptFrom,
  client,
  unwrap,
} from "./client";
import type {
  Agent,
  AgentRole,
  BriefingFile,
  DecisionInput,
  FleetInstructions,
  FleetInstructionsKind,
  FleetInstructionsVersion,
  Grant,
  HarnessConfig,
  HarnessConfigScope,
  HarnessSettings,
  PendingAuthorization,
  PendingDeviceAuthorization,
  SetupScript,
  SupervisorStatus,
  UpdateAgentRoleInput,
  VariantVersion,
} from "./types";

// The server's default is fleet, so that kind goes without the parameter.
function kindQuery(kind: FleetInstructionsKind) {
  return kind === "fleet" ? {} : { kind };
}

export const agentsApi = {
  // Archived agents are left out unless asked for. Anything that names an
  // agent from history asks for them, so archiving never changes a name.
  async listAgents(opts: { includeArchived?: boolean } = {}): Promise<Agent[]> {
    return (
      await unwrap(
        client.GET("/api/agents", {
          params: { query: { include_archived: opts.includeArchived } },
        }),
      )
    ).agents;
  },

  // getFleetInstructions lists the versions; archived ones only with
  // includeArchived.
  async getFleetInstructions(
    includeArchived = false,
    kind: FleetInstructionsKind = "fleet",
  ): Promise<FleetInstructions> {
    return await unwrap(
      client.GET("/api/fleet_instructions", {
        params: {
          query: {
            ...(includeArchived && { include_archived: true }),
            ...kindQuery(kind),
          },
        },
      }),
    );
  },

  // The archive calls hide a version, or every version of a name, from the
  // list; the text is kept. Owners only. The current default is refused (409).
  async archiveFleetInstructionsVersion(
    id: string,
  ): Promise<FleetInstructionsVersion> {
    return await unwrap(
      client.POST("/api/fleet_instructions/versions/{id}/archive", {
        params: { path: { id } },
      }),
    );
  },

  async unarchiveFleetInstructionsVersion(
    id: string,
  ): Promise<FleetInstructionsVersion> {
    return await unwrap(
      client.POST("/api/fleet_instructions/versions/{id}/unarchive", {
        params: { path: { id } },
      }),
    );
  },

  // Both group calls return how many versions changed.
  async archiveFleetInstructionsGroup(
    name: string,
    kind: FleetInstructionsKind = "fleet",
  ): Promise<number> {
    return (
      await unwrap(
        client.POST("/api/fleet_instructions/groups/archive", {
          params: { query: kindQuery(kind) },
          body: { name },
        }),
      )
    ).versions;
  },

  async unarchiveFleetInstructionsGroup(
    name: string,
    kind: FleetInstructionsKind = "fleet",
  ): Promise<number> {
    return (
      await unwrap(
        client.POST("/api/fleet_instructions/groups/unarchive", {
          params: { query: kindQuery(kind) },
          body: { name },
        }),
      )
    ).versions;
  },

  // getFleetBriefings is the briefing files the server would write to a
  // workspace, rendered with the given saved version's fleet instructions, or
  // with none when versionId is null.
  async getFleetBriefings(versionId: string | null): Promise<BriefingFile[]> {
    const query = versionId === null ? {} : { version_id: versionId };
    return (
      await unwrap(
        client.GET("/api/fleet_instructions/briefings", { params: { query } }),
      )
    ).files;
  },

  async saveFleetInstructionsVersion(
    name: string,
    body: string,
    makeDefault: boolean,
    kind: FleetInstructionsKind = "fleet",
  ): Promise<FleetInstructionsVersion> {
    return await unwrap(
      client.POST("/api/fleet_instructions/versions", {
        params: { query: kindQuery(kind) },
        body: { name, body, make_default: makeDefault },
      }),
    );
  },

  // setFleetInstructionsDefault chooses the version agents are given; null
  // chooses none, which leaves their briefings as they are without any.
  async setFleetInstructionsDefault(
    versionId: string | null,
    kind: FleetInstructionsKind = "fleet",
  ): Promise<void> {
    await unwrap(
      client.PUT("/api/fleet_instructions/default", {
        params: { query: kindQuery(kind) },
        body: { version_id: versionId },
      }),
    );
  },

  // listVariants is owners only: a 403 is how a member finds out they are
  // not one. Archived versions only with includeArchived.
  async listVariants(includeArchived = false): Promise<VariantVersion[]> {
    return (
      await unwrap(
        client.GET("/api/variants", {
          params: { query: includeArchived ? { include_archived: true } : {} },
        }),
      )
    ).versions;
  },

  // saveVariantVersion adds the next version of a name. fleetVersionId null
  // pins no fleet instructions.
  async saveVariantVersion(
    name: string,
    harness: VariantVersion["harness"],
    settings: Record<string, string>,
    fleetVersionId: string | null,
  ): Promise<VariantVersion> {
    return await unwrap(
      client.POST("/api/variants/versions", {
        body: { name, harness, settings, fleet_instructions_version_id: fleetVersionId },
      }),
    );
  },

  async archiveVariantVersion(id: string): Promise<VariantVersion> {
    return await unwrap(
      client.POST("/api/variants/versions/{id}/archive", { params: { path: { id } } }),
    );
  },

  async unarchiveVariantVersion(id: string): Promise<VariantVersion> {
    return await unwrap(
      client.POST("/api/variants/versions/{id}/unarchive", { params: { path: { id } } }),
    );
  },

  // Both group calls return how many versions changed.
  async archiveVariantGroup(name: string): Promise<number> {
    return (
      await unwrap(client.POST("/api/variants/groups/archive", { body: { name } }))
    ).versions;
  },

  async unarchiveVariantGroup(name: string): Promise<number> {
    return (
      await unwrap(client.POST("/api/variants/groups/unarchive", { body: { name } }))
    ).versions;
  },

  // setAgentVariant puts an agent on a variant version, or off its variant
  // with null. Owners only.
  async setAgentVariant(agentId: string, versionId: string | null): Promise<void> {
    await unwrap(
      client.PUT("/api/agents/{id}/variant", {
        params: { path: { id: agentId } },
        body: { version_id: versionId },
      }),
    );
  },

  // createAgentSetupScript registers an agent and returns a shell script
  // that configures a workspace for it -- the path for anyone without a
  // slop-haus checkout and a Go toolchain.
  //
  // The body is a shell script, not JSON, and the filename the server chose
  // comes back in a header. It is returned for the caller to hand straight
  // to a download rather than put on screen.
  async createAgentSetupScript(name: string): Promise<SetupScript> {
    return setupScriptFrom(
      client.POST("/api/agents/setup_script", {
        body: { name },
        parseAs: "text",
      }),
    );
  },

  // rotateAgentSetupScript returns a new setup script for an existing
  // agent and, in the same request, signs out every workspace currently
  // signed in as it -- until the new script's device sign-in is approved,
  // nothing can act as that agent.
  async rotateAgentSetupScript(id: string): Promise<SetupScript> {
    return setupScriptFrom(
      client.POST("/api/agents/{id}/setup_script", {
        params: { path: { id } },
        parseAs: "text",
      }),
    );
  },

  async setAgentDisabled(id: string, disabled: boolean): Promise<Agent> {
    return await unwrap(
      client.PATCH("/api/agents/{id}", {
        params: { path: { id } },
        body: { disabled },
      }),
    );
  },

  // Archiving is for a disabled agent; it stays disabled when unarchived.
  async archiveAgent(id: string): Promise<Agent> {
    return await unwrap(client.POST("/api/agents/{id}/archive", { params: { path: { id } } }));
  },

  async unarchiveAgent(id: string): Promise<Agent> {
    return await unwrap(client.POST("/api/agents/{id}/unarchive", { params: { path: { id } } }));
  },

  // -- Agent roles --
  //
  // Every route here is a signed-in person's, never an agent's (see
  // role_handler.go's requireSignedInUser) -- a role gates which tickets an
  // agent may claim, so letting an agent reach any of these would let it
  // grant itself work.

  async listAgentRoles(): Promise<AgentRole[]> {
    return (await unwrap(client.GET("/api/agent-roles"))).roles;
  },

  async createAgentRole(name: string, description = ""): Promise<AgentRole> {
    return await unwrap(
      client.POST("/api/agent-roles", { body: { name, description } }),
    );
  },

  async updateAgentRole(
    id: string,
    input: UpdateAgentRoleInput,
  ): Promise<AgentRole> {
    return await unwrap(
      client.PATCH("/api/agent-roles/{id}", {
        params: { path: { id } },
        body: input,
      }),
    );
  },

  // deleteAgentRole throws a RoleInUseError (see errorFrom) while tickets
  // require the role; clearTickets=true removes it from them in the same
  // request instead of refusing.
  async deleteAgentRole(id: string, clearTickets = false): Promise<void> {
    await unwrap(
      client.DELETE("/api/agent-roles/{id}", {
        params: {
          path: { id },
          query: { clear_tickets: clearTickets || undefined },
        },
      }),
    );
  },

  // setAgentRoles replaces an agent's whole set and returns it -- cheaper
  // than working out an add/remove diff client-side, and matches the
  // server's own replace-not-patch semantics (SetAgentRoles).
  async setAgentRoles(agentId: string, roleIds: string[]): Promise<Agent> {
    return await unwrap(
      client.PUT("/api/agents/{id}/roles", {
        params: { path: { id: agentId } },
        body: { role_ids: roleIds },
      }),
    );
  },

  // -- Harness config --
  //
  // A person's to read and set, never an agent's: it decides what runs in
  // every agent's process. Each save replaces the scope's whole set.

  async getSupervisorStatus(): Promise<SupervisorStatus> {
    return await unwrap(client.GET("/api/supervisor/status"));
  },

  async getHarnessConfig(): Promise<HarnessConfig> {
    return await unwrap(client.GET("/api/harness_config"));
  },

  async saveHarnessConfigDefaults(
    settings: HarnessSettings,
  ): Promise<HarnessConfigScope> {
    return await unwrap(
      client.PUT("/api/harness_config", { body: { settings } }),
    );
  },

  async saveAgentHarnessConfig(
    agentId: string,
    settings: HarnessSettings,
  ): Promise<HarnessConfigScope> {
    return await unwrap(
      client.PUT("/api/agents/{id}/harness_config", {
        params: { path: { id: agentId } },
        body: { settings },
      }),
    );
  },

  // removeAgentHarnessConfig deletes the agent's override, so it follows the
  // tenant's defaults again.
  async removeAgentHarnessConfig(agentId: string): Promise<void> {
    await unwrap(
      client.DELETE("/api/agents/{id}/harness_config", {
        params: { path: { id: agentId } },
      }),
    );
  },

  // -- OAuth --
  //
  // getAuthRequest/decideAuthRequest back the /authorize consent page;
  // lookupDevice/decideDevice back /device. Both need a signed-in user
  // (server/internal/oauth/api.go's authn.RequireUser), same as everything
  // else in this client -- there is no "public" mode here the way
  // getAuthConfig uses, because there is nothing for either page to show
  // an anonymous visitor.

  async getAuthRequest(id: string): Promise<PendingAuthorization> {
    return unwrap(
      client.GET("/api/oauth/requests/{id}", { params: { path: { id } } }),
    );
  },

  async decideAuthRequest(
    id: string,
    decision: DecisionInput,
  ): Promise<string> {
    const body = await unwrap(
      client.POST("/api/oauth/requests/{id}/decision", {
        params: { path: { id } },
        body: decision,
      }),
    );
    return body.redirect_to;
  },

  async lookupDevice(userCode: string): Promise<PendingDeviceAuthorization> {
    return unwrap(
      client.POST("/api/oauth/device/lookup", {
        body: { user_code: userCode },
      }),
    );
  },

  async decideDevice(userCode: string, decision: DecisionInput): Promise<void> {
    await unwrap(
      client.POST("/api/oauth/device/decision", {
        body: { user_code: userCode, ...decision },
      }),
    );
  },

  // listGrants/revokeGrant back the Agents panel's "Authorizations" list --
  // every client currently able to act as one of this tenant's agents, and
  // the one way to cut one off before its tokens expire on their own.
  async listGrants(): Promise<Grant[]> {
    return (await unwrap(client.GET("/api/oauth/grants"))).grants;
  },

  async revokeGrant(id: string): Promise<void> {
    await unwrap(
      client.DELETE("/api/oauth/grants/{id}", { params: { path: { id } } }),
    );
  },
};
