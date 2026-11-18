import { afterEach, describe, expect, it, vi } from "vitest";
import * as api from "@/api";
import { CSRF_HEADER } from "@/api";
import { sendUsage, USAGE_PATH } from "./send";

afterEach(() => vi.restoreAllMocks());

describe("sendUsage", () => {
  it("posts the batch to the usage route with the session's write headers, and keepalive for a closing page", () => {
    vi.spyOn(api, "sessionWriteHeaders").mockReturnValue({ [CSRF_HEADER]: "csrf-token", "X-Tenant-ID": "tenant-1" });
    const fetchMock = vi.fn(async () => new Response(null, { status: 204 }));
    vi.stubGlobal("fetch", fetchMock);

    sendUsage([{ name: "new_ticket", attrs: { screen: "board", action: "open_form" } }]);

    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe("/api/usage/events");
    expect(USAGE_PATH).toBe("/api/usage/events");
    expect(init.method).toBe("POST");
    expect(init.keepalive).toBe(true);
    expect(init.credentials).toBe("same-origin");
    expect(init.headers).toMatchObject({ "Content-Type": "application/json", [CSRF_HEADER]: "csrf-token", "X-Tenant-ID": "tenant-1" });
    expect(JSON.parse(init.body as string)).toEqual({ events: [{ name: "new_ticket", attrs: { screen: "board", action: "open_form" } }] });
  });

  it("does not surface a failed request", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => Promise.reject(new Error("offline"))));
    expect(() => sendUsage([])).not.toThrow();
    await Promise.resolve();
  });
});
