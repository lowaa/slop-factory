import type { APIRequestContext } from "@playwright/test";

import { FAKE_GITHUB_URL } from "@/support/env";
import { expect } from "./fixtures";
import { connect, newPrivateKey, type Scripted, scriptGitHub } from "./github";

// The repository the workspace manifest maps a spec's ticket paths to, on the
// e2e stack's fake GitHub (fake_github_server.mjs).
export const REPO_PATH = "attempts";
const OWNER = "e2e-org";
const NAME = "attempts";
export const REMOTE = `github.com/${OWNER}/${NAME}`;

export const MAIN_SHA = "1111111111111111111111111111111111111111";
// What a pull request's head is at once its branch is pushed (nothing is
// pushed in e2e: the fake just says so).
export const HEAD_SHA = (attempt: number) => "c".repeat(39) + attempt;
export const MERGE_SHA = "9999999999999999999999999999999999999999";

// The fake's pull request numbers: an attempt's change is #100 + its number.
export const pullNumber = (attempt: number) => 100 + attempt;
export const pullUrl = (attempt: number) => `https://github.com/${OWNER}/${NAME}/pull/${pullNumber(attempt)}`;
export const attemptBranch = (ticket: number, attempt: number) => `ticket-${ticket}-a${attempt}`;
export const baseBranch = (ticket: number) => `slop-haus-base-ticket-${ticket}`;

// compareKey names a comparison for the fake's script: its three-dot range.
export const compareKey = (base: string, head: string) => `compare:${base}...${head}`;

export function comparison(mergeBase: string, extra: Record<string, unknown> = {}) {
  return { body: { merge_base_commit: { sha: mergeBase }, base_commit: { sha: MAIN_SHA }, behind_by: 0, status: "ahead", files: [], ...extra } };
}

const key = newPrivateKey();

// The workspace manifest is the tenant's and the database cleanup does not
// reach it, so a spec that needs one clears it on both sides of every test:
// workspace.spec.ts expects none. The credential is the same.
export async function useFakeCodeHost(api: APIRequestContext): Promise<void> {
  await removeFakeCodeHost(api);
  const manifest = await api.put("/api/workspace/manifest", { data: { repos: [{ path: REPO_PATH, remote: REMOTE }] } });
  expect(manifest.ok(), await manifest.text()).toBeTruthy();
  const connected = await connect(api, { app_id: 4242, installation_id: 777, private_key: key });
  expect(connected.status(), await connected.text()).toBe(200);
}

export async function removeFakeCodeHost(api: APIRequestContext): Promise<void> {
  await api.delete("/api/workspace/manifest");
  await api.delete("/api/codehost/github-app");
}

// scriptTicket scripts the fake GitHub for a ticket whose attempts are
// claimed and handed over: main at MAIN_SHA, a pull request for each of the
// first `attempts` attempts (the first promoted, onto main; the rest onto the
// ticket's base branch), green checks, and a comparison that says every head
// branched from MAIN_SHA. `overrides` replace any of it by script key.
// Scripting resets the fake's recorded requests and branches, so a spec does it
// once, before the first claim.
export async function scriptTicket(request: APIRequestContext, ticket: number, attempts: number, overrides: Scripted = {}) {
  const script: Scripted = {
    repo: { body: { default_branch: "main" } },
    token: {
      status: 201,
      body: { token: "ghs_e2e_fake", expires_at: new Date(Date.now() + 3600_000).toISOString(), repositories: [{ full_name: `${OWNER}/${NAME}` }] },
    },
    compare: comparison(MAIN_SHA),
    checkRuns: { body: { total_count: 1, check_runs: [{ id: 1, name: "ci", status: "completed", conclusion: "success" }] } },
    commitStatus: { body: { total_count: 0, statuses: [] } },
    graphql: { body: { data: {} } },
  };
  for (let n = 1; n <= attempts; n++) {
    const promoted = n === 1;
    script[`openPull:${attemptBranch(ticket, n)}`] = { status: 201, body: { number: pullNumber(n) } };
    script[`pull:${pullNumber(n)}`] = {
      body: { number: pullNumber(n), node_id: `PR_${n}`, draft: !promoted, head: { sha: HEAD_SHA(n) }, base: { ref: promoted ? "main" : baseBranch(ticket) } },
    };
    script[`merge:${pullNumber(n)}`] = { body: { merged: true, sha: MERGE_SHA } };
  }
  await scriptGitHub(request, { ...script, ...overrides, branches: { main: MAIN_SHA } });
}

export interface GitHubRequest {
  method: string;
  path: string;
  authorization: string;
  body: string;
}

export async function githubRequests(request: APIRequestContext): Promise<GitHubRequest[]> {
  const resp = await request.get(`${FAKE_GITHUB_URL}/_admin/requests`);
  return (await resp.json()) as GitHubRequest[];
}

export function bodyOf(r: GitHubRequest): Record<string, unknown> {
  return JSON.parse(r.body) as Record<string, unknown>;
}

// The branches the server asked the fake to create, in order, with the commit
// each was asked at.
export function createdBranches(requests: GitHubRequest[]): { branch: string; sha: string }[] {
  return requests
    .filter((r) => r.method === "POST" && /^\/repos\/[^/]+\/[^/]+\/git\/refs$/.test(r.path))
    .filter((r) => String(bodyOf(r).ref).startsWith("refs/heads/"))
    .map((r) => ({ branch: String(bodyOf(r).ref).replace(/^refs\/heads\//, ""), sha: String(bodyOf(r).sha) }));
}

export function openedPulls(requests: GitHubRequest[]): Record<string, unknown>[] {
  return requests.filter((r) => r.method === "POST" && /\/pulls$/.test(r.path)).map(bodyOf);
}
