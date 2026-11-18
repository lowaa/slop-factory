import { generateKeyPairSync } from "node:crypto";

import type { APIRequestContext } from "@playwright/test";

import { DEV_USER_PASSWORD, FAKE_GITHUB_URL } from "@/support/env";
import { expect } from "./fixtures";

export function newPrivateKey(): string {
  const { privateKey } = generateKeyPairSync("rsa", {
    modulusLength: 2048,
    privateKeyEncoding: { type: "pkcs1", format: "pem" },
    publicKeyEncoding: { type: "spki", format: "pem" },
  });
  return privateKey;
}

export type Scripted = Record<string, { status?: number; body: unknown }>;

// The fake's script: route answers, plus `branches` ({name: sha}), the
// branches that exist before the spec starts.
export async function scriptGitHub(request: APIRequestContext, script: Scripted | Record<string, unknown>) {
  const resp = await request.post(`${FAKE_GITHUB_URL}/_admin/script`, { data: script });
  expect(resp.ok(), await resp.text()).toBeTruthy();
}

// connect puts the credential, confirming the password only when the session
// is too old to count (see intake.spec.ts's createSource for why not always).
export async function connect(api: APIRequestContext, data: Record<string, unknown>) {
  let resp = await api.put("/api/codehost/github-app", { data });
  if (resp.status() === 403 || resp.status() === 401) {
    const reauth = await api.post("/api/auth/reauth/password", { data: { password: DEV_USER_PASSWORD } });
    expect(reauth.ok(), await reauth.text()).toBeTruthy();
    resp = await api.put("/api/codehost/github-app", { data });
  }
  return resp;
}

