import { describe, expect, it } from "vitest";
import { apiClient } from "@/api";
import { assertNoUnstubbedApiCalls, guardApi, stubApi } from "./story_harness";

describe("story harness API guard", () => {
  it("fails an unstubbed call naming the method, and again at the end of the story", async () => {
    const restore = guardApi();
    try {
      await expect(apiClient.listTenants()).rejects.toThrow("apiClient.listTenants was called in a story without a stub");
      expect(() => assertNoUnstubbedApiCalls()).toThrow("apiClient.listTenants called without a stub");
      // The record is cleared once reported.
      expect(() => assertNoUnstubbedApiCalls()).not.toThrow();
    } finally {
      restore();
    }
  });

  it("guards every method of the client, not just one", async () => {
    const restore = guardApi();
    try {
      const names = Object.entries(apiClient)
        .filter(([, value]) => typeof value === "function")
        .map(([name]) => name);
      expect(names.length).toBeGreaterThan(50);
      for (const name of names) {
        const call = (apiClient as unknown as Record<string, () => Promise<unknown>>)[name];
        await expect(call()).rejects.toThrow(`apiClient.${name} was called`);
      }
      expect(() => assertNoUnstubbedApiCalls()).toThrow();
    } finally {
      restore();
    }
  });

  it("lets a stubbed call through and restores the real method afterwards", async () => {
    const real = apiClient.listTenants;
    const restore = guardApi();
    stubApi({ listTenants: async () => [] });
    await expect(apiClient.listTenants()).resolves.toEqual([]);
    expect(() => assertNoUnstubbedApiCalls()).not.toThrow();
    restore();
    expect(apiClient.listTenants).toBe(real);
  });
});
