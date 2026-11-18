import { afterEach, describe, expect, it, vi } from "vitest";
import { BuildTag, UNKNOWN_BUILD_TAG } from "./build_tag";

function stubFetch(respond: () => Response | Promise<Response>) {
  const fetchMock = vi.fn(async (_url: string) => respond());
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

afterEach(() => vi.unstubAllGlobals());

describe("BuildTag", () => {
  it("reads the tag from /version.json", async () => {
    const fetchMock = stubFetch(() => new Response(JSON.stringify({ version: "20261003T000000Z-abc12345" })));
    await expect(new BuildTag().get()).resolves.toBe("20261003T000000Z-abc12345");
    expect(fetchMock.mock.calls[0][0]).toBe("/version.json");
  });

  it("asks once, however often it is asked", async () => {
    const fetchMock = stubFetch(() => new Response(JSON.stringify({ version: "t1" })));
    const tag = new BuildTag();
    await Promise.all([tag.get(), tag.get()]);
    await tag.get();
    expect(fetchMock).toHaveBeenCalledOnce();
  });

  it.each([
    ["a non-OK response", () => new Response("nope", { status: 404 })],
    ["a body that isn't JSON", () => new Response("<html>")],
    ["a version that isn't a string", () => new Response(JSON.stringify({ version: 7 }))],
    ["a version outside the tag alphabet", () => new Response(JSON.stringify({ version: "Hemoglobin 13.5" }))],
  ])("is unknown for %s", async (_name, respond) => {
    stubFetch(respond);
    await expect(new BuildTag().get()).resolves.toBe(UNKNOWN_BUILD_TAG);
  });

  it("is unknown when the request fails", async () => {
    stubFetch(() => Promise.reject(new TypeError("Failed to fetch")));
    await expect(new BuildTag().get()).resolves.toBe(UNKNOWN_BUILD_TAG);
  });
});

describe("BuildTag at app start", () => {
  it("reads when constructed, and keeps that tag when version.json changes later", async () => {
    let version = "old-build";
    const fetchMock = stubFetch(() => new Response(JSON.stringify({ version })));
    const tag = new BuildTag();
    expect(fetchMock).toHaveBeenCalledOnce();
    await tag.get();
    version = "new-build";
    await expect(tag.get()).resolves.toBe("old-build");
    expect(tag.peek()).toBe("old-build");
    expect(fetchMock).toHaveBeenCalledOnce();
  });

  it("reports a newer server tag only once one was noted", async () => {
    stubFetch(() => new Response(JSON.stringify({ version: "t1" })));
    const tag = new BuildTag();
    expect(tag.serverTag()).toBeUndefined();
    tag.noteServerTag("t2");
    expect(tag.serverTag()).toBe("t2");
  });
});
