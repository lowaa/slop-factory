import { createServer } from "node:http";
import type { AddressInfo } from "node:net";

import { expect, request, test } from "@playwright/test";

import { waitUntilReachable } from "./reachable";

async function serve(status: () => number, body: string) {
  const server = createServer((_req, res) => {
    res.statusCode = status();
    res.end(body);
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  const baseURL = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
  return { baseURL, stop: () => new Promise<void>((resolve) => server.close(() => resolve())) };
}

test("returns once the path answers 2xx, after failing before", async () => {
  let calls = 0;
  const srv = await serve(() => (++calls < 3 ? 503 : 200), "ok");
  const ctx = await request.newContext({ baseURL: srv.baseURL });
  try {
    await waitUntilReachable(ctx, "/healthz", 5_000, 5);
    expect(calls).toBe(3);
  } finally {
    await ctx.dispose();
    await srv.stop();
  }
});

test("says whose error it was: the status, url and body of the last answer", async () => {
  const srv = await serve(() => 404, "404 page not found");
  const ctx = await request.newContext({ baseURL: srv.baseURL });
  try {
    await expect(waitUntilReachable(ctx, "/healthz", 50, 5)).rejects.toThrow(
      new RegExp(`/healthz never became reachable: status 404 from ${srv.baseURL}/healthz: 404 page not found`),
    );
  } finally {
    await ctx.dispose();
    await srv.stop();
  }
});
