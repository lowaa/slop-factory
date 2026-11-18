import { describe, expect, it } from "vitest";
import { createApiGuard } from "./api_guard";

function makeClient() {
  return {
    async listThings(): Promise<string[]> {
      return ["real"];
    },
    async getThing(id: number): Promise<string> {
      return `real ${id}`;
    },
    baseUrl: "http://example.invalid",
  };
}

describe("createApiGuard", () => {
  it("fails an unstubbed call naming the method, and again at the end of the story", async () => {
    const client = makeClient();
    const { guard, assertNoUnstubbedCalls } = createApiGuard(client, { clientName: "apiClient", stubName: "stubApi" });
    const restore = guard();
    try {
      await expect(client.listThings()).rejects.toThrow("apiClient.listThings was called in a story without a stub");
      expect(() => assertNoUnstubbedCalls()).toThrow("apiClient.listThings called without a stub: add stubApi({...})");
      // The record is cleared once reported.
      expect(() => assertNoUnstubbedCalls()).not.toThrow();
    } finally {
      restore();
    }
  });

  it("fails the story even when the component swallowed the error", async () => {
    const client = makeClient();
    const { guard, assertNoUnstubbedCalls } = createApiGuard(client);
    const restore = guard();
    try {
      await client.getThing(1).catch(() => "an error state");
      expect(() => assertNoUnstubbedCalls()).toThrow("client.getThing called without a stub");
    } finally {
      restore();
    }
  });

  it("guards every method of the client and leaves other properties alone", async () => {
    const client = makeClient();
    const { guard } = createApiGuard(client);
    const restore = guard();
    try {
      await expect(client.listThings()).rejects.toThrow("client.listThings was called");
      await expect(client.getThing(2)).rejects.toThrow("client.getThing was called");
      expect(client.baseUrl).toBe("http://example.invalid");
    } finally {
      restore();
    }
  });

  it("lets a stubbed call through and restores the real method afterwards", async () => {
    const client = makeClient();
    const real = client.listThings;
    const { guard, stub, assertNoUnstubbedCalls } = createApiGuard(client);
    const restore = guard();
    stub({ listThings: async () => [] });
    await expect(client.listThings()).resolves.toEqual([]);
    expect(() => assertNoUnstubbedCalls()).not.toThrow();
    restore();
    expect(client.listThings).toBe(real);
    await expect(client.getThing(3)).resolves.toBe("real 3");
  });

  it("forgets calls recorded before the guard went on", async () => {
    const client = makeClient();
    const { guard, assertNoUnstubbedCalls } = createApiGuard(client);
    guard();
    await client.listThings().catch(() => undefined);
    guard()();
    expect(() => assertNoUnstubbedCalls()).not.toThrow();
  });

  it("guards methods a class client inherits, and restores them", async () => {
    class Client {
      async fetchThing(): Promise<string> {
        return "real";
      }
    }
    const client = new Client();
    const { guard, assertNoUnstubbedCalls } = createApiGuard(client);
    const restore = guard();
    await expect(client.fetchThing()).rejects.toThrow("client.fetchThing was called");
    expect(() => assertNoUnstubbedCalls()).toThrow("client.fetchThing called");
    restore();
    expect(Object.hasOwn(client, "fetchThing")).toBe(false);
    await expect(client.fetchThing()).resolves.toBe("real");
  });
});
