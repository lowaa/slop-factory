import type { APIRequestContext } from "@playwright/test";

// Polls `path` until it answers 2xx. The error says what the last attempt
// got, and for a status the body too: it says whose error it is (traefik's
// "404 page not found", nginx's HTML, ...) where a bare status can't.
export async function waitUntilReachable(
  ctx: APIRequestContext,
  path: string,
  timeoutMs = 30_000,
  pollMs = 500,
): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  let lastError: string | undefined;
  while (Date.now() < deadline) {
    try {
      const resp = await ctx.get(path);
      if (resp.ok()) {
        return;
      }
      lastError = `status ${resp.status()} from ${resp.url()}: ${(await resp.text()).slice(0, 200)}`;
    } catch (err) {
      lastError = String(err);
    }
    await new Promise((resolve) => setTimeout(resolve, pollMs));
  }
  throw new Error(`${path} never became reachable: ${lastError}`);
}
