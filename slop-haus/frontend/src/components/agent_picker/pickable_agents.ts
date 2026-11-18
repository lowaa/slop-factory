import { apiClient } from "@/api";
import { readAgents } from "@/lib/shared_resources";

// What the sign-in pages offer as "an existing agent": just enough to pick one.
export interface PickableAgent {
  id: string;
  name: string;
}

// listPickableAgents is the enabled agents a person can sign a client in as:
// the team's workers, and their own conductors. Someone else's conductor is
// not offered, and the server would refuse it. Each listing stands alone: one
// that fails leaves what the other found, and neither failing leaves "a new
// agent" as the only choice.
export async function listPickableAgents(): Promise<PickableAgent[]> {
  const [agents, conductors] = await Promise.all([
    readAgents({ archived: false }).catch(() => []),
    apiClient.listConductors().then((l) => l.own, () => []),
  ]);
  return [...agents, ...conductors]
    .filter((a) => a.disabled_at === null && a.archived_at === null)
    .map(({ id, name }) => ({ id, name }));
}
