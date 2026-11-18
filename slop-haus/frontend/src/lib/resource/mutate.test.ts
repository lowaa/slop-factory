import { describe, expect, it } from "vitest";
import { createResourceClient, mutateResource, updateResource, type ResourceKey } from ".";

const KEY: ResourceKey = ["test", "counter"];

function deferred<T>() {
  let resolve: (v: T) => void = () => {};
  let reject: (err: unknown) => void = () => {};
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

describe("mutateResource", () => {
  it("shows the optimistic value while the request is in flight, then folds in the reply", async () => {
    const client = createResourceClient();
    client.setQueryData(KEY, { n: 1 });
    const reply = deferred<number>();
    const done = mutateResource<{ n: number }, number>(
      KEY,
      { optimistic: () => ({ n: 2 }), request: () => reply.promise, commit: (_cur, n) => ({ n }) },
      client,
    );
    expect(client.getQueryData(KEY)).toEqual({ n: 2 });
    reply.resolve(3);
    await done;
    expect(client.getQueryData(KEY)).toEqual({ n: 3 });
  });

  it("rolls the optimistic value back when the request fails, and rethrows", async () => {
    const client = createResourceClient();
    client.setQueryData(KEY, { n: 1 });
    const reply = deferred<number>();
    const done = mutateResource<{ n: number }, number>(
      KEY,
      { optimistic: () => ({ n: 2 }), request: () => reply.promise, commit: (_cur, n) => ({ n }) },
      client,
    );
    reply.reject(new Error("boom"));
    await expect(done).rejects.toThrow("boom");
    expect(client.getQueryData(KEY)).toEqual({ n: 1 });
  });

  it("lets rollback undo only its own change, leaving a later write alone", async () => {
    const client = createResourceClient();
    client.setQueryData(KEY, { a: 1, b: 1 });
    const reply = deferred<number>();
    const done = mutateResource<{ a: number; b: number }, number>(
      KEY,
      {
        optimistic: (cur) => ({ ...cur, a: 2 }),
        request: () => reply.promise,
        commit: (cur) => cur,
        rollback: (cur, before) => ({ ...cur, a: before.a }),
      },
      client,
    );
    client.setQueryData(KEY, { a: 2, b: 9 });
    reply.reject(new Error("boom"));
    await expect(done).rejects.toThrow("boom");
    expect(client.getQueryData(KEY)).toEqual({ a: 1, b: 9 });
  });
});

describe("updateResource", () => {
  it("keeps what it wrote when it cancels a read in flight", async () => {
    const client = createResourceClient();
    client.setQueryData(KEY, { n: 1 });
    const slow = deferred<{ n: number }>();
    void client.fetchQuery({ queryKey: KEY, queryFn: () => slow.promise, staleTime: 0 }).catch(() => {});
    updateResource<{ n: number }>(KEY, () => ({ n: 2 }), client);
    await new Promise((resolve) => setTimeout(resolve, 0));
    slow.resolve({ n: 0 });
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(client.getQueryData(KEY)).toEqual({ n: 2 });
  });

  it("leaves a read in flight alone when the write changes nothing", async () => {
    const client = createResourceClient();
    const current = { n: 1 };
    client.setQueryData(KEY, current);
    const slow = deferred<{ n: number }>();
    const read = client.fetchQuery({ queryKey: KEY, queryFn: () => slow.promise, staleTime: 0 });
    updateResource<{ n: number }>(KEY, (c) => c as { n: number }, client);
    slow.resolve({ n: 5 });
    await read;
    expect(client.getQueryData(KEY)).toEqual({ n: 5 });
  });
});
