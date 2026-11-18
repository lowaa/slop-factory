import type { components } from "@/api_generated/schema";
import { client, unwrap } from "./client";

type Schemas = components["schemas"];

export type CompareRequest = Schemas["CompareBody"];
export type CompareResult = Schemas["Result"];
export type ComparePair = Schemas["Pair"];
export type CompareRow = Schemas["Row"];
export type CompareSide = Schemas["Side"];
export type CompareMeasure = Schemas["MeasureInfo"];
export type CompareSummary = Schemas["Summary"];
export type CompareVerdicts = Schemas["Verdicts"];

export const compareApi = {
  // compareVariants reads and writes nothing: it is a POST for the size of
  // its body. A 422 is a malformed request or a set over the server's bound.
  async compareVariants(request: CompareRequest): Promise<CompareResult> {
    return await unwrap(client.POST("/api/compare", { body: request }));
  },
};
