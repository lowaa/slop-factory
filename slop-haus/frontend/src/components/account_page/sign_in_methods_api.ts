import type { SignInMethodsApi } from "@slop-factory/auth";
import { apiClient } from "@/api";

// signInMethodsApi adapts this app's client to the shared panel's adapter.
// A password here always signs in with the account's own address (the
// panel never asks for one), and adding one and changing it are the same
// PUT.
export const signInMethodsApi: SignInMethodsApi = {
  async list() {
    const body = await apiClient.listSignInMethods();
    return {
      password: body.password ? { email: body.password.email, verified: body.password.verified } : null,
      identities: body.identities,
      passwordLoginEnabled: body.password_login_enabled,
    };
  },
  addPassword: ({ password }) => apiClient.setPassword(password),
  changePassword: (password) => apiClient.setPassword(password),
  removePassword: () => apiClient.removePassword(),
  removeIdentity: ({ provider, subject }) => apiClient.removeIdentity(provider, subject),
  startLink: (provider, next) => apiClient.startLink(provider, next),
  startReauth: (provider, next) => apiClient.startReauth(provider, next),
  reauthWithPassword: (password) => apiClient.reauthWithPassword(password),
};
