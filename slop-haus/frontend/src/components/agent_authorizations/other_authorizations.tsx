import type { Grant } from "@/api";
import AgentAuthorizations from "./agent_authorizations";
import type { GrantsHandle } from "./use_grants";

interface Props {
  grants: Grant[];
  handle: GrantsHandle;
}

// OtherAuthorizations is the grants whose agent has no row on the page. Every
// grant belongs to an agent, so this is empty unless a page's roster is
// missing one; it is shown rather than dropped, under the agent's name and
// id, so a credential never goes unseen.
export default function OtherAuthorizations({ grants, handle }: Props) {
  if (grants.length === 0) return null;
  const byAgent = new Map<string, Grant[]>();
  for (const grant of grants) byAgent.set(grant.agent_id, [...(byAgent.get(grant.agent_id) ?? []), grant]);
  return (
    <section className="agent-authorizations__other" aria-label="Unlinked authorizations">
      <h2>Unlinked authorizations</h2>
      <p>These act as an agent that has no row here.</p>
      {[...byAgent].map(([agentId, own]) => (
        <div key={agentId}>
          <h3>
            {own[0].agent_name || "Unknown agent"} <code>{agentId}</code>
          </h3>
          <AgentAuthorizations grants={own} handle={handle} />
        </div>
      ))}
    </section>
  );
}
