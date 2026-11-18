import { createContext, useContext } from "react";

// The lookup AgentNamesProvider fills in, apart from the components so the
// component file exports only components (fast refresh needs that).
export const AgentNamesContext = createContext<Record<string, string>>({});

// shortId is what shows until a name is known -- enough to tell agents
// apart without a 36-character uuid taking over the line.
export function shortId(id: string): string {
  return id.slice(0, 8);
}

export function useAgentName(id: string): string {
  return useContext(AgentNamesContext)[id] ?? shortId(id);
}
