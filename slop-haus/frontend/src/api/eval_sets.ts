import type { components } from "@/api_generated/schema";
import { client, unwrap } from "./client";

type Schemas = components["schemas"];

export type EvalSet = Schemas["EvalSetBody"];
export type EvalSetFilter = Schemas["FilterBody"];
export type EvalSetDefinition = Schemas["EvalSetDefinitionBody"];
export type EvalSetBatch = Schemas["EvalSetBatchBody"];
export type EvalSetRunResult = Schemas["RunEvalSetResponseBody"];

export const evalSetsApi = {
  async listEvalSets(): Promise<EvalSet[]> {
    return (await unwrap(client.GET("/api/eval_sets"))).eval_sets;
  },

  // A name another set has is a 409.
  async createEvalSet(definition: EvalSetDefinition): Promise<EvalSet> {
    return await unwrap(client.POST("/api/eval_sets", { body: definition }));
  },

  async replaceEvalSet(id: string, definition: EvalSetDefinition): Promise<EvalSet> {
    return await unwrap(client.PUT("/api/eval_sets/{id}", { params: { path: { id } }, body: definition }));
  },

  async deleteEvalSet(id: string): Promise<void> {
    await unwrap(client.DELETE("/api/eval_sets/{id}", { params: { path: { id } } }));
  },

  // pinEvalSetMatches turns a filter set into the tickets it matches now.
  async pinEvalSetMatches(id: string): Promise<EvalSet> {
    return await unwrap(client.POST("/api/eval_sets/{id}/pin", { params: { path: { id } } }));
  },

  // The batches run across the set, newest first.
  async listEvalSetBatches(id: string): Promise<EvalSetBatch[]> {
    return (await unwrap(client.GET("/api/eval_sets/{id}/batches", { params: { path: { id } } }))).batches;
  },

  // runEvalSet is addReplayBatch over the set's tickets: confirmAttempts is
  // the number the person confirmed, and the server refuses (422) a run it
  // does not equal. Tickets it could not replay come back in refused.
  async runEvalSet(
    id: string,
    versionIds: (string | null)[],
    runs: number,
    confirmAttempts: number,
  ): Promise<EvalSetRunResult> {
    return await unwrap(
      client.POST("/api/eval_sets/{id}/runs", {
        params: { path: { id } },
        body: { version_ids: versionIds, runs, confirm_attempts: confirmAttempts },
      }),
    );
  },
};
