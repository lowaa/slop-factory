import { Database } from "@e2e-support/db";

import { DATABASE_URL } from "@/support/env";

const database = new Database(DATABASE_URL);

export const execute = database.execute.bind(database);
export const query = database.query.bind(database);
export const count = database.count.bind(database);
export const transaction = database.transaction.bind(database);
