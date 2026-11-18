import {
  client,
  unwrap,
} from "./client";
import type {
  ConnectGitHubApp,
  CreateIntakeSource,
  GitHubAppConnection,
  GitHubAppCreated,
  GitHubAppInstalled,
  GitHubAppInstalledRequest,
  GitHubAppManifestStart,
  GitHubAppStatus,
  GitHubAppVerification,
  GitHubAppWebhook,
  GitHubAppWebhookCheck,
  GitHubWebhooks,
  IntakeCredential,
  IntakeSources,
  OnboardingStatus,
  ManifestRepo,
  RepoMove,
  RepoSync,
  SavedWorkspaceManifest,
  Workspace,
} from "./types";

export const integrationsApi = {
  // getOnboardingStatus is the setup a tenant still needs before workers can
  // do code work: a repo declared and a code host connected.
  async getOnboardingStatus(): Promise<OnboardingStatus> {
    return await unwrap(client.GET("/api/onboarding"));
  },

  async getWorkspace(): Promise<Workspace> {
    return await unwrap(client.GET("/api/workspace"));
  },

  async setWorkspaceManifest(
    repos: ManifestRepo[],
  ): Promise<SavedWorkspaceManifest> {
    return await unwrap(
      client.PUT("/api/workspace/manifest", { body: { repos } }),
    );
  },

  // moveWorkspaceRepo changes a manifest repo's path and rewrites the scopes
  // under it; dryRun lists the changes and writes nothing.
  async moveWorkspaceRepo(
    fromPath: string,
    toPath: string,
    dryRun: boolean,
  ): Promise<RepoMove> {
    return await unwrap(
      client.POST("/api/workspace/manifest/move", {
        body: { from_path: fromPath, to_path: toPath, dry_run: dryRun },
      }),
    );
  },

  // syncWorkspaceRepo merges a manifest repo's default branch into its base
  // branch on the code host. A conflict is not an error: the result says
  // conflict and names the ticket filed to resolve it.
  async syncWorkspaceRepo(path: string): Promise<RepoSync> {
    return await unwrap(
      client.POST("/api/workspace/manifest/sync", { body: { path } }),
    );
  },

  async clearWorkspaceManifest(): Promise<void> {
    await unwrap(client.DELETE("/api/workspace/manifest"));
  },

  // -- GitHub webhooks --

  async listGitHubWebhooks(): Promise<GitHubWebhooks> {
    return await unwrap(client.GET("/api/github/webhooks"));
  },

  // -- Code host credential --

  async getGitHubApp(): Promise<GitHubAppStatus> {
    return await unwrap(client.GET("/api/codehost/github-app"));
  },

  // connectGitHubApp sends the App's private key. It is never read back: the
  // answer has the key's fingerprint, not the key.
  async connectGitHubApp(body: ConnectGitHubApp): Promise<GitHubAppConnection> {
    return await unwrap(client.PUT("/api/codehost/github-app", { body }));
  },

  // beginGitHubAppManifest asks the server for the manifest and the GitHub URL
  // to post it to; the browser is then sent there (see lib/navigation.ts).
  async beginGitHubAppManifest(
    account: string,
  ): Promise<GitHubAppManifestStart> {
    return await unwrap(
      client.POST("/api/codehost/github-app/manifest", { body: { account } }),
    );
  },

  async completeGitHubAppManifest(
    code: string,
    state: string,
  ): Promise<GitHubAppCreated> {
    return await unwrap(
      client.POST("/api/codehost/github-app/manifest/complete", {
        body: { code, state },
      }),
    );
  },

  async getGitHubAppInstallURL(): Promise<string> {
    return (await unwrap(client.POST("/api/codehost/github-app/install-url")))
      .install_url;
  },

  async completeGitHubAppInstall(
    body: GitHubAppInstalledRequest,
  ): Promise<GitHubAppInstalled> {
    return await unwrap(
      client.POST("/api/codehost/github-app/install/complete", { body }),
    );
  },

  async verifyGitHubApp(): Promise<GitHubAppVerification> {
    return await unwrap(client.POST("/api/codehost/github-app/verify"));
  },

  async getGitHubAppWebhook(): Promise<GitHubAppWebhook> {
    return await unwrap(client.GET("/api/codehost/github-app/webhook"));
  },

  // checkGitHubAppWebhook has the server read the App's webhook from GitHub
  // and set it right. takeOver replaces a URL on another host: only for an
  // App no other environment uses.
  async checkGitHubAppWebhook(
    takeOver = false,
  ): Promise<GitHubAppWebhookCheck> {
    return await unwrap(
      client.POST("/api/codehost/github-app/webhook/check", {
        params: { query: takeOver ? { take_over: true } : {} },
      }),
    );
  },

  async rotateGitHubAppWebhookSecret(): Promise<GitHubAppWebhook> {
    return await unwrap(client.POST("/api/codehost/github-app/webhook/rotate"));
  },

  async removeGitHubApp(): Promise<void> {
    await unwrap(client.DELETE("/api/codehost/github-app"));
  },

  // -- Intake --

  async listIntakeSources(): Promise<IntakeSources> {
    return await unwrap(client.GET("/api/intake/sources"));
  },

  async createIntakeSource(
    body: CreateIntakeSource,
  ): Promise<IntakeCredential> {
    return await unwrap(client.POST("/api/intake/sources", { body }));
  },

  async rotateIntakeSource(id: string): Promise<IntakeCredential> {
    return await unwrap(
      client.POST("/api/intake/sources/{id}/rotate", {
        params: { path: { id } },
      }),
    );
  },

  async revokeIntakeSource(id: string): Promise<void> {
    await unwrap(
      client.DELETE("/api/intake/sources/{id}", { params: { path: { id } } }),
    );
  },
};
