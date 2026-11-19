import {
  client,
  unwrap,
} from "./client";
import type {
  NamedIntegrationLock,
  QueueBlockageState,
  ServerSettingHistory,
  Setting,
  TenantSettingHistory,
  TenantUserSetting,
  UpdateSettingInput,
  UserSetting,
} from "./types";

export const settingsApi = {
  async listSettings(): Promise<Setting[]> {
    return (await unwrap(client.GET("/api/settings"))).settings;
  },

  async listIntegrationLocks(): Promise<NamedIntegrationLock[]> {
    return (await unwrap(client.GET("/api/integration_locks"))).locks;
  },

  // name is a lock's name as listed by listIntegrationLocks; absent is the
  // default lock.
  async forceUnlockIntegration(name?: string): Promise<void> {
    await unwrap(client.DELETE("/api/integration_lock/holder", { params: { query: { name } } }));
  },

  async dropIntegrationLockWaiter(agentId: string, name?: string): Promise<void> {
    await unwrap(
      client.DELETE("/api/integration_lock/queue/{agent_id}", {
        params: { path: { agent_id: agentId }, query: { name } },
      }),
    );
  },

  // blockage is null when nothing is waiting on scopes; lockingOff says that
  // is because the tenant has turned scope locking off.
  async getQueueBlockage(): Promise<QueueBlockageState> {
    const body = await unwrap(client.GET("/api/queue/blockage"));
    return { blockage: body.blockage, lockingOff: body.locking_off };
  },

  async listMySettings(): Promise<UserSetting[]> {
    return (await unwrap(client.GET("/api/me/settings"))).settings;
  },

  async setMyBoolSetting(name: string, value: boolean): Promise<UserSetting> {
    return await unwrap(
      client.PUT("/api/me/settings/{name}", {
        params: { path: { name } },
        body: { type: "bool", bool_val: value },
      }),
    );
  },

  async listMyTenantSettings(): Promise<TenantUserSetting[]> {
    return (await unwrap(client.GET("/api/me/tenant-settings"))).settings;
  },

  async setMyTenantBoolSetting(
    name: string,
    value: boolean,
  ): Promise<TenantUserSetting> {
    return await unwrap(
      client.PUT("/api/me/tenant-settings/{name}", {
        params: { path: { name } },
        body: { type: "bool", bool_val: value },
      }),
    );
  },

  // The signed-in user's own changes to a setting in this tenant. Newest
  // first; pass the previous page's next_before as before to continue.
  async getMyTenantSettingHistory(
    name: string,
    page: { limit?: number; before?: number } = {},
  ): Promise<TenantSettingHistory> {
    return await unwrap(
      client.GET("/api/me/tenant-settings/{name}/history", {
        params: { path: { name }, query: page },
      }),
    );
  },

  // Newest first; pass the previous page's next_before as before to continue.
  async getSettingHistory(
    name: string,
    page: { limit?: number; before?: number } = {},
  ): Promise<ServerSettingHistory> {
    return await unwrap(
      client.GET("/api/settings/{name}/history", {
        params: { path: { name }, query: page },
      }),
    );
  },

  async getSetting(name: string): Promise<Setting> {
    return await unwrap(
      client.GET("/api/settings/{name}", { params: { path: { name } } }),
    );
  },

  async updateSetting(
    name: string,
    input: UpdateSettingInput,
  ): Promise<Setting> {
    return await unwrap(
      client.PATCH("/api/settings/{name}", {
        params: { path: { name } },
        body: input,
      }),
    );
  },
};
