import { describeOperations, type Operation } from "./operations_harness";

const filter = { statuses: ["done"], q: "login" };
const set = {
  id: "11111111-1111-1111-1111-111111111111",
  name: "Logins",
  description: "",
  filter,
  ticket_ids: [] as number[],
  created_by: null,
  created_at: "2026-10-04T12:00:00Z",
  updated_at: "2026-10-04T12:00:00Z",
};
const definition = { name: "Logins", description: "", filter };
const batch = {
  batch_id: "22222222-2222-2222-2222-222222222222",
  created_at: "2026-10-05T12:00:00Z",
  created_by: null,
  runs: 1,
  ticket_count: 2,
  version_ids: [null, "v1"],
};
const path = `/api/eval_sets/${set.id}`;

const operations: Record<string, Operation> = {
  listEvalSets: {
    call: (c) => c.listEvalSets(),
    method: "GET",
    url: "/api/eval_sets",
    response: { eval_sets: [set] },
    result: [set],
  },
  createEvalSet: {
    call: (c) => c.createEvalSet(definition),
    method: "POST",
    url: "/api/eval_sets",
    body: definition,
    response: set,
  },
  replaceEvalSet: {
    call: (c) => c.replaceEvalSet(set.id, definition),
    method: "PUT",
    url: path,
    body: definition,
    response: set,
  },
  deleteEvalSet: { call: (c) => c.deleteEvalSet(set.id), method: "DELETE", url: path },
  pinEvalSetMatches: {
    call: (c) => c.pinEvalSetMatches(set.id),
    method: "POST",
    url: `${path}/pin`,
    response: { ...set, filter: null, ticket_ids: [1, 2] },
  },
  listEvalSetBatches: {
    call: (c) => c.listEvalSetBatches(set.id),
    method: "GET",
    url: `${path}/batches`,
    response: { batches: [batch] },
    result: [batch],
  },
  runEvalSet: {
    call: (c) => c.runEvalSet(set.id, [null, "v1"], 2, 8),
    method: "POST",
    url: `${path}/runs`,
    body: { version_ids: [null, "v1"], runs: 2, confirm_attempts: 8 },
    response: { batch_id: batch.batch_id, added: [{ ticket_id: 1, attempts: [2, 3] }], refused: [] },
  },
};

describeOperations("eval sets operations", () => import("./eval_sets").then((m) => m.evalSetsApi), operations);
