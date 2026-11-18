import { describe, expect, it } from "vitest";
import { installFetch } from "./fetch_recorder";

describe("apiClient", () => {
  // The per-resource objects are spread into apiClient, so an operation
  // name in two of them would silently shadow one with the other.
  it("is the merge of the per-resource objects, none shadowing another", async () => {
    const { api } = await installFetch();
    const resources = await Promise.all([
      import("./session").then((m) => m.sessionApi),
      import("./tickets").then((m) => m.ticketsApi),
      import("./attachments").then((m) => m.attachmentsApi),
      import("./agents").then((m) => m.agentsApi),
      import("./conversations").then((m) => m.conversationsApi),
      import("./settings").then((m) => m.settingsApi),
      import("./integrations").then((m) => m.integrationsApi),
      import("./compare").then((m) => m.compareApi),
      import("./reports").then((m) => m.reportsApi),
      import("./proposals").then((m) => m.proposalsApi),
      import("./steward").then((m) => m.stewardApi),
      import("./provisioning").then((m) => m.provisioningApi),
      import("./landing").then((m) => m.landingApi),
      import("./eval_sets").then((m) => m.evalSetsApi),
    ]);
    const names = resources.flatMap((resource) => Object.keys(resource));
    expect(Object.keys(api.apiClient).sort()).toEqual(names.sort());
    expect(new Set(names).size).toBe(names.length);
  });
});
