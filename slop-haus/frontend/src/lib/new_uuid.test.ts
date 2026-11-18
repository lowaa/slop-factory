import { afterEach, describe, expect, it, vi } from "vitest";
import { newUuid } from "./new_uuid";

const UUID_V4 = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;

describe("newUuid", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("makes a v4 UUID, different each time", () => {
    const a = newUuid();
    expect(a).toMatch(UUID_V4);
    expect(newUuid()).not.toBe(a);
  });

  it("does not need crypto.randomUUID, which an http page lacks", () => {
    vi.stubGlobal("crypto", { getRandomValues: globalThis.crypto.getRandomValues.bind(globalThis.crypto) });
    expect(newUuid()).toMatch(UUID_V4);
  });

  it("still makes one where there is no crypto at all", () => {
    vi.stubGlobal("crypto", undefined);
    expect(newUuid()).toMatch(UUID_V4);
  });
});
