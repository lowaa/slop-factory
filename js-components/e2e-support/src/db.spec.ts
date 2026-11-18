import { expect, test } from "@playwright/test";

import { Database, type DbClient } from "./db";

// Records what it is sent; fails the statements the test names. It decides
// nothing else.
class RecordingClient implements DbClient {
  readonly calls: [string, unknown[] | undefined][] = [];
  ended = false;

  constructor(
    private readonly failOn: string[] = [],
    private readonly rows: unknown[] = [],
  ) {}

  async connect(): Promise<void> {}

  async query(sql: string, params?: unknown[]): Promise<{ rows: unknown[] }> {
    this.calls.push([sql, params]);
    if (this.failOn.includes(sql)) throw new Error(`boom: ${sql}`);
    return { rows: this.rows };
  }

  async end(): Promise<void> {
    this.ended = true;
  }
}

test.describe("transaction", () => {
  test("runs each statement with its own params, between BEGIN and COMMIT", async () => {
    const client = new RecordingClient();
    await new Database("x", () => client).transaction([["a $1", [1]], ["b"]]);
    expect(client.calls).toEqual([
      ["BEGIN", undefined],
      ["a $1", [1]],
      ["b", []],
      ["COMMIT", undefined],
    ]);
    expect(client.ended).toBe(true);
  });

  test("rolls back and throws the statement's error, not the rollback's", async () => {
    const client = new RecordingClient(["b", "ROLLBACK"]);
    await expect(new Database("x", () => client).transaction([["a"], ["b"], ["c"]])).rejects.toThrow("boom: b");
    expect(client.calls.map(([sql]) => sql)).toEqual(["BEGIN", "a", "b", "ROLLBACK"]);
    expect(client.ended).toBe(true);
  });
});

test.describe("deadlock retry", () => {
  const noSleep = async () => {};
  const deadlock = () => Object.assign(new Error("deadlock detected"), { code: "40P01" });

  // Fails the first `failures` queries with the given error, on separate clients.
  function flaky(failures: number, makeErr: () => Error) {
    const clients: RecordingClient[] = [];
    let left = failures;
    const factory = () => {
      const c = new RecordingClient();
      const q = c.query.bind(c);
      c.query = async (sql, params) => {
        if (left > 0) {
          left--;
          throw makeErr();
        }
        return q(sql, params);
      };
      clients.push(c);
      return c;
    };
    return { clients, factory };
  }

  test("re-runs a statement the server aborted as a deadlock victim, closing every client", async () => {
    const { clients, factory } = flaky(2, deadlock);
    await new Database("x", factory, noSleep).execute("TRUNCATE t", [1]);
    expect(clients).toHaveLength(3);
    expect(clients.every((c) => c.ended)).toBe(true);
    expect(clients[2].calls).toEqual([["TRUNCATE t", [1]]]);
  });

  test("backs off longer after each deadlock", async () => {
    const { factory } = flaky(3, deadlock);
    const waits: number[] = [];
    await new Database("x", factory, async (ms) => void waits.push(ms)).execute("x");
    expect(waits).toEqual([100, 200, 300]);
  });

  test("re-runs a query and returns its rows", async () => {
    const { clients, factory } = flaky(1, deadlock);
    await new Database("x", factory, noSleep).query("SELECT 1");
    expect(clients).toHaveLength(2);
  });

  test("replays a whole transaction from BEGIN after a deadlock", async () => {
    const { clients, factory } = flaky(1, deadlock);
    await new Database("x", factory, noSleep).transaction([["a"], ["b"]]);
    expect(clients).toHaveLength(2);
    expect(clients[1].calls.map(([sql]) => sql)).toEqual(["BEGIN", "a", "b", "COMMIT"]);
  });

  test("gives up after repeated deadlocks", async () => {
    const { clients, factory } = flaky(100, deadlock);
    await expect(new Database("x", factory, noSleep).execute("TRUNCATE t")).rejects.toThrow("deadlock detected");
    expect(clients).toHaveLength(5);
  });

  test("does not retry other errors", async () => {
    const { clients, factory } = flaky(1, () => new Error("syntax"));
    await expect(new Database("x", factory, noSleep).execute("bad")).rejects.toThrow("syntax");
    expect(clients).toHaveLength(1);
  });
});

test.describe("count and query", () => {
  test("count reads the first row's count as a number (pg sends bigint as a string)", async () => {
    const client = new RecordingClient([], [{ count: "42" }]);
    expect(await new Database("x", () => client).count("SELECT count(*) FROM t WHERE a = $1", [7])).toBe(42);
    expect(client.calls).toEqual([["SELECT count(*) FROM t WHERE a = $1", [7]]]);
    expect(client.ended).toBe(true);
  });

  test("a failing query still closes the client", async () => {
    const client = new RecordingClient(["q"]);
    await expect(new Database("x", () => client).query("q")).rejects.toThrow("boom: q");
    expect(client.ended).toBe(true);
  });
});
