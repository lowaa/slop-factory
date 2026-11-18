import { Client } from "pg";

export type Statement = [sql: string, params?: unknown[]];

// The part of pg's Client this wrapper uses, so a test can stand in for it.
export interface DbClient {
  connect(): Promise<unknown>;
  query(sql: string, params?: unknown[]): Promise<{ rows: unknown[] }>;
  end(): Promise<void>;
}

const MAX_DEADLOCK_ATTEMPTS = 5;
const DEADLOCK_BACKOFF_MS = 100;

function isDeadlock(err: unknown): boolean {
  return (err as { code?: unknown } | null)?.code === "40P01";
}

// Small wrapper around `pg`: a suite's use of the database is one statement
// at a time (truncate-before-each, a count asserting the backend created
// what it should), so this avoids the connect/query/end dance at each call.
export class Database {
  constructor(
    private readonly connectionString: string,
    private readonly newClient: (connectionString: string) => DbClient = (url) => new Client({ connectionString: url }),
    private readonly sleep: (ms: number) => Promise<void> = (ms) => new Promise((resolve) => setTimeout(resolve, ms)),
  ) {}

  // Postgres aborts one party of a lock cycle with SQLSTATE 40P01 and says the
  // statement is safe to retry. The cleanup's TRUNCATE takes ACCESS EXCLUSIVE
  // locks on many tables while the server under test may still be finishing a
  // request from the previous test, locking some of them in another order, so
  // an occasional cycle can't be designed away from here. The aborted work
  // left nothing behind, so it is re-run on a fresh connection.
  private async withClient<T>(run: (client: DbClient) => Promise<T>): Promise<T> {
    for (let attempt = 1; ; attempt++) {
      try {
        return await this.runOnce(run);
      } catch (err) {
        if (!isDeadlock(err) || attempt >= MAX_DEADLOCK_ATTEMPTS) throw err;
        // Backing off lets the other side of the cycle finish its transaction.
        await this.sleep(DEADLOCK_BACKOFF_MS * attempt);
      }
    }
  }

  private async runOnce<T>(run: (client: DbClient) => Promise<T>): Promise<T> {
    const client = this.newClient(this.connectionString);
    await client.connect();
    try {
      return await run(client);
    } finally {
      await client.end();
    }
  }

  async execute(sql: string, params: unknown[] = []): Promise<void> {
    await this.withClient((client) => client.query(sql, params));
  }

  async query<T extends Record<string, unknown>>(sql: string, params: unknown[] = []): Promise<T[]> {
    return this.withClient(async (client) => (await client.query(sql, params)).rows as T[]);
  }

  // The first column of the first row of a `SELECT count(*)`.
  async count(sql: string, params: unknown[] = []): Promise<number> {
    const rows = await this.query<{ count: string }>(sql, params);
    // node-postgres returns bigint as a string, since a count can exceed what
    // a JS number represents exactly.
    return Number(rows[0].count);
  }

  // Runs the statements in order in one transaction, each with its own
  // parameters (a parameterised statement can't be sent more than one to a
  // query, so a multi-statement script can't carry any). If one fails the
  // earlier ones are rolled back and the failure is what is thrown.
  async transaction(statements: Statement[]): Promise<void> {
    await this.withClient(async (client) => {
      await client.query("BEGIN");
      try {
        for (const [sql, params] of statements) await client.query(sql, params ?? []);
        await client.query("COMMIT");
      } catch (err) {
        // A failed rollback must not hide why the transaction failed.
        await client.query("ROLLBACK").catch(() => {});
        throw err;
      }
    });
  }
}
