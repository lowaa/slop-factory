import AgentName from "@/shared/names/agent_name";
import MemberName from "@/shared/names/member_name";
import { authorKind } from "./author_kind";

interface AuthorNameProps {
  agentId: string | null;
  userId: string | null;
  // The server's flag for a person whose account was deleted.
  userDeleted?: boolean;
}

// Renders whichever of the three an author actually is, so call sites
// don't each re-derive "no agent id used to mean the human" -- see
// author_kind.ts for why that stopped being true.
export default function AuthorName({ agentId, userId, userDeleted = false }: AuthorNameProps) {
  const kind = authorKind(agentId, userId, userDeleted);
  if (kind === "agent") return <AgentName id={agentId!} />;
  if (kind === "user") return <MemberName id={userId} />;
  if (kind === "deleted") return <MemberName id={null} deleted />;
  // Comments always have one author or the other (see api/index.ts's
  // Comment doc comment); neither would be a notice from the server
  // itself, not a person to name.
  return <span className="author-name--notice">System</span>;
}
