import type { components } from "@/api_generated/schema";
import { client, unwrap } from "./client";

export type LandingEntry = components["schemas"]["LandingEntryBody"];
export type LandingCapabilities = components["schemas"]["CapabilitiesBody"];

export const landingApi = {
  // In landing order, then the changes handed back.
  async listLandingQueue(): Promise<LandingEntry[]> {
    return (await unwrap(client.GET("/api/landing/queue"))).entries;
  },

  async getLandingCapabilities(): Promise<LandingCapabilities> {
    return await unwrap(client.GET("/api/landing/capabilities"));
  },
};
