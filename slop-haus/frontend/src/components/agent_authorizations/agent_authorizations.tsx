import { Button, Tooltip } from "@slop-factory/ui";
import type { Grant } from "@/api";
import Timestamp from "@/shared/timestamp/timestamp";
import type { GrantsHandle } from "./use_grants";
import "./agent_authorizations.scss";

// lastUsedLabel says when a client last actually used the credential it was
// handed, or that it never has -- a grant approved five minutes ago and one
// approved a year ago and never touched again look identical without this.
function LastUsed({ at }: { at: string | null }) {
  return at ? (
    <>
      last used <Timestamp at={at} />
    </>
  ) : (
    <>never used</>
  );
}

interface Props {
  grants: Grant[];
  handle: GrantsHandle;
}

// AgentAuthorizations is the clients able to act as one agent, under that
// agent's row, each with a Revoke. An agent with none says so: an archived
// one should always read this way.
export default function AgentAuthorizations({ grants, handle }: Props) {
  const { state, revoke, revokingId, revokeError } = handle;
  if (state.status === "loading") return <p className="agent-authorizations__note">Loading authorizations…</p>;
  if (state.status === "error") {
    return (
      <p role="alert" className="agent-authorizations__note agent-authorizations__note--error">
        {state.message}
      </p>
    );
  }
  return (
    <div className="agent-authorizations" role="group" aria-label="Authorizations">
      {grants.length === 0 ? (
        <p className="agent-authorizations__note">No authorizations</p>
      ) : (
        <ul className="agent-authorizations__list">
          {grants.map((grant) => (
            <li key={grant.id} className="agent-authorizations__item">
              <span className="agent-authorizations__client">{grant.client_name}</span>
              <span className="agent-authorizations__meta">
                created <Timestamp at={grant.created_at} />
              </span>
              <span className="agent-authorizations__meta">
                <LastUsed at={grant.last_used_at} />
              </span>
              <Tooltip text="Revoke this authorization: its access token stops working immediately.">
                <Button
                  size="sm"
                  variant="danger"
                  onClick={() => void revoke(grant.id)}
                  disabled={revokingId === grant.id}
                >
                  {revokingId === grant.id ? "Revoking…" : "Revoke"}
                </Button>
              </Tooltip>
            </li>
          ))}
        </ul>
      )}
      {revokeError && grants.some((g) => g.id === revokeError.grantId) && (
        <p role="alert" className="agent-authorizations__note agent-authorizations__note--error">
          {revokeError.message}
        </p>
      )}
    </div>
  );
}
