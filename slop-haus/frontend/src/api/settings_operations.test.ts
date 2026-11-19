import * as fx from "@/test/fixtures";
import { describeOperations, type Operation, setting } from "./operations_harness";

const blockage = {
  ticket_id: 9,
  ticket_title: "Waiting ticket",
  holder_ticket_id: 4,
  holder_title: "Holder ticket",
  holder_status: "in_progress",
  holder_agent_name: "agent-1",
  path: "server/",
};
const userSetting = fx.makeTenantUserSetting({ name: "s" });

const operations: Record<string, Operation> = {
  getQueueBlockage: {
    call: (c) => c.getQueueBlockage(),
    method: "GET",
    url: "/api/queue/blockage",
    response: { blockage, locking_off: false },
    result: { blockage, lockingOff: false },
  },
  listIntegrationLocks: { call: (c) => c.listIntegrationLocks(), method: "GET", url: "/api/integration_locks", response: { locks: [] }, result: [] },
  forceUnlockIntegration: {
    call: (c) => c.forceUnlockIntegration(),
    method: "DELETE",
    url: "/api/integration_lock/holder",
  },
  dropIntegrationLockWaiter: {
    call: (c) => c.dropIntegrationLockWaiter("a-1"),
    method: "DELETE",
    url: "/api/integration_lock/queue/a-1",
  },
  listSettings: { call: (c) => c.listSettings(), method: "GET", url: "/api/settings", response: { settings: [setting] }, result: [setting] },
  getSettingHistory: {
    call: (c) => c.getSettingHistory("s", { limit: 2, before: 9 }),
    method: "GET",
    url: "/api/settings/s/history?limit=2&before=9",
    response: { changes: [] },
  },
  getMyTenantSettingHistory: {
    call: (c) => c.getMyTenantSettingHistory("s", { limit: 2, before: 9 }),
    method: "GET",
    url: "/api/me/tenant-settings/s/history?limit=2&before=9",
    response: { changes: [] },
  },
  getSetting: { call: (c) => c.getSetting("s"), method: "GET", url: "/api/settings/s", response: setting },
  updateSetting: {
    call: (c) => c.updateSetting("s", { type: "bool", bool_val: true }),
    method: "PATCH",
    url: "/api/settings/s",
    body: { type: "bool", bool_val: true },
    response: setting,
  },
  listMySettings: {
    call: (c) => c.listMySettings(),
    method: "GET",
    url: "/api/me/settings",
    response: { settings: [userSetting] },
    result: [userSetting],
  },
  setMyBoolSetting: {
    call: (c) => c.setMyBoolSetting("remote_control.session_switch_explained", false),
    method: "PUT",
    url: "/api/me/settings/remote_control.session_switch_explained",
    body: { type: "bool", bool_val: false },
    response: userSetting,
  },
  listMyTenantSettings: {
    call: (c) => c.listMyTenantSettings(),
    method: "GET",
    url: "/api/me/tenant-settings",
    response: { settings: [userSetting] },
    result: [userSetting],
  },
  setMyTenantBoolSetting: {
    call: (c) => c.setMyTenantBoolSetting("auto_approve", true),
    method: "PUT",
    url: "/api/me/tenant-settings/auto_approve",
    body: { type: "bool", bool_val: true },
    response: userSetting,
  },
};

describeOperations("settings operations", () => import("./settings").then((m) => m.settingsApi), operations);
