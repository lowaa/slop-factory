import { describeOperations, type Operation } from "./operations_harness";

const entry = {
  ticket_id: 7,
  attempt_number: 1,
  change_url: "https://example.test/pr/1",
  state: "landing" as const,
  position: 1,
  step: "" as const,
  back_reason: "",
  batch_id: null,
  batch_position: 0,
  since_update_seconds: 0,
};
const capabilities = { merge_method: "worker" as const, merge_queue_enabled: true, change_tools: true, merge_queue_available: true };

const operations: Record<string, Operation> = {
  listLandingQueue: {
    call: (c) => c.listLandingQueue(),
    method: "GET",
    url: "/api/landing/queue",
    response: { entries: [entry] },
    result: [entry],
  },
  getLandingCapabilities: {
    call: (c) => c.getLandingCapabilities(),
    method: "GET",
    url: "/api/landing/capabilities",
    response: capabilities,
    result: capabilities,
  },
};

describeOperations("landing operations", () => import("./landing").then((m) => m.landingApi), operations);
