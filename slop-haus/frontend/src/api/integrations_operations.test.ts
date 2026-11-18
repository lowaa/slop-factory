import * as fx from "@/test/fixtures";
import { describeOperations, type Operation } from "./operations_harness";

const credential = {
  app_id: 1,
  app_slug: "acme-app",
  installation_id: 2,
  pending: false,
  key_fingerprint: "ab12",
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
  verified_at: null,
};

const operations: Record<string, Operation> = {
  getOnboardingStatus: {
    call: (c) => c.getOnboardingStatus(),
    method: "GET",
    url: "/api/onboarding",
    response: { steps: [], code_work_ready: true },
  },
  getWorkspace: {
    call: (c) => c.getWorkspace(),
    method: "GET",
    url: "/api/workspace",
    response: { manifest: null, can_edit: true, proposal: null, agents: [], divergences: [] },
  },
  setWorkspaceManifest: {
    call: (c) => c.setWorkspaceManifest([{ path: "app", remote: "github.com/acme/app" }]),
    method: "PUT",
    url: "/api/workspace/manifest",
    body: { repos: [{ path: "app", remote: "github.com/acme/app" }] },
    response: { repos: [{ path: "app", remote: "github.com/acme/app" }], revision: 1, updated_at: "2026-10-04T12:00:00Z", base_branches: [] },
  },
  moveWorkspaceRepo: {
    call: (c) => c.moveWorkspaceRepo("old", "app", true),
    method: "POST",
    url: "/api/workspace/manifest/move",
    body: { from_path: "old", to_path: "app", dry_run: true },
    response: { applied: false, from_path: "old", to_path: "app", repos: [], tickets: [], intake_sources: [], warnings: [] },
  },
  syncWorkspaceRepo: {
    call: (c) => c.syncWorkspaceRepo("app"),
    method: "POST",
    url: "/api/workspace/manifest/sync",
    body: { path: "app" },
    response: { path: "app", base_branch: "team-a", default_branch: "main", outcome: "merged", head: "abc" },
  },
  clearWorkspaceManifest: { call: (c) => c.clearWorkspaceManifest(), method: "DELETE", url: "/api/workspace/manifest" },
  listGitHubWebhooks: {
    call: (c) => c.listGitHubWebhooks(),
    method: "GET",
    url: "/api/github/webhooks",
    response: { enabled: true, deliveries: [] },
  },
  getGitHubApp: {
    call: (c) => c.getGitHubApp(),
    method: "GET",
    url: "/api/codehost/github-app",
    response: { enabled: true, can_edit: true, credential: null },
  },
  connectGitHubApp: {
    call: (c) => c.connectGitHubApp({ app_id: 1, installation_id: 2, private_key: "pem" }),
    method: "PUT",
    url: "/api/codehost/github-app",
    body: { app_id: 1, installation_id: 2, private_key: "pem" },
    response: credential,
  },
  beginGitHubAppManifest: {
    call: (c) => c.beginGitHubAppManifest("acme"),
    method: "POST",
    url: "/api/codehost/github-app/manifest",
    body: { account: "acme" },
    response: { action_url: "https://github.com/x", manifest: "{}" },
  },
  completeGitHubAppManifest: {
    call: (c) => c.completeGitHubAppManifest("code", "state"),
    method: "POST",
    url: "/api/codehost/github-app/manifest/complete",
    body: { code: "code", state: "state" },
    response: { credential, install_url: "https://github.com/y" },
  },
  getGitHubAppInstallURL: {
    call: (c) => c.getGitHubAppInstallURL(),
    method: "POST",
    url: "/api/codehost/github-app/install-url",
    response: { install_url: "https://github.com/y" },
    result: "https://github.com/y",
  },
  completeGitHubAppInstall: {
    call: (c) => c.completeGitHubAppInstall({ installation_id: 7, setup_action: "install", state: "s" }),
    method: "POST",
    url: "/api/codehost/github-app/install/complete",
    body: { installation_id: 7, setup_action: "install", state: "s" },
    response: { credential, requested: false },
  },
  verifyGitHubApp: {
    call: (c) => c.verifyGitHubApp(),
    method: "POST",
    url: "/api/codehost/github-app/verify",
    response: { account: "acme", repos: [], manifest: [] },
  },
  getGitHubAppWebhook: {
    call: (c) => c.getGitHubAppWebhook(),
    method: "GET",
    url: "/api/codehost/github-app/webhook",
    response: fx.makeAppWebhook(),
  },
  checkGitHubAppWebhook: {
    call: (c) => c.checkGitHubAppWebhook(),
    method: "POST",
    url: "/api/codehost/github-app/webhook/check",
    response: { outcome: "unchanged", webhook: fx.makeAppWebhook() },
  },
  rotateGitHubAppWebhookSecret: {
    call: (c) => c.rotateGitHubAppWebhookSecret(),
    method: "POST",
    url: "/api/codehost/github-app/webhook/rotate",
    response: fx.makeAppWebhook(),
  },
  removeGitHubApp: { call: (c) => c.removeGitHubApp(), method: "DELETE", url: "/api/codehost/github-app" },
  listIntakeSources: {
    call: (c) => c.listIntakeSources(),
    method: "GET",
    url: "/api/intake/sources",
    response: { enabled: true, hmac: true, sources: [], deliveries: [] },
  },
  createIntakeSource: {
    call: (c) =>
      c.createIntakeSource({ name: "desk", adapter: "native", trust: "untrusted", auth: "bearer" }),
    method: "POST",
    url: "/api/intake/sources",
    body: { name: "desk", adapter: "native", trust: "untrusted", auth: "bearer" },
    response: { source: fx.makeIntakeSource({ name: "desk" }), token: "shi_x" },
  },
  rotateIntakeSource: {
    call: (c) => c.rotateIntakeSource("s1"),
    method: "POST",
    url: "/api/intake/sources/s1/rotate",
    response: { source: fx.makeIntakeSource(), token: "shi_y" },
  },
  revokeIntakeSource: { call: (c) => c.revokeIntakeSource("s1"), method: "DELETE", url: "/api/intake/sources/s1" },
};

describeOperations("integrations operations", () => import("./integrations").then((m) => m.integrationsApi), operations);
