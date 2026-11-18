import { apiClient } from "@/api";
import { readAgents } from "@/lib/shared_resources";
import type { PickableAgent } from "@/components/agent_picker";

// listReplaceableAgents is what a new agent can take over from: the team's
// workers and consoles and the person's own conductors that are not archived,
// enabled or not -- a lost credential usually belongs to an agent that is
// still enabled, and one already switched off is just as replaceable. Each
// listing stands alone, as in listPickableAgents.
export async function listReplaceableAgents(): Promise<PickableAgent[]> {
  const [agents, conductors] = await Promise.all([
    readAgents({ archived: false }).catch(() => []),
    apiClient.listConductors().then((l) => l.own, () => []),
  ]);
  return [...agents, ...conductors]
    .filter((a) => a.archived_at === null && a.replaced_by === null)
    .map(({ id, name }) => ({ id, name }));
}
