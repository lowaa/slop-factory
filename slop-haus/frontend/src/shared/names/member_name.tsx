import { useMemo, type ReactNode } from "react";
import type { Member } from "@/api";
import { useMemberNames } from "@/lib/shared_resources";
import { CurrentUserIdContext, type MemberNames, MemberNamesContext, useMemberName } from "./member_names";

// Mirrors agent_name.tsx's AgentNamesProvider: one lookup for the whole
// signed-in shell rather than a fetch per comment/message, refreshed
// occasionally so someone who joins the tenant while the board is open
// gets named without a reload.
const REFRESH_MS = 60_000;

interface MemberNamesProviderProps {
  // The signed-in user's id -- required, since this provider only ever
  // mounts inside the authenticated shell (GET /api/members needs a
  // session), unlike AgentNamesProvider which has nothing analogous to
  // thread through.
  currentUserId: string;
  children: ReactNode;
}

export function MemberNamesProvider({ currentUserId, children }: MemberNamesProviderProps) {
  // A failed fetch keeps the names already known, as AgentNamesProvider does.
  const { data } = useMemberNames({ pollMs: REFRESH_MS });
  const names = useMemo<MemberNames>(() => {
    const byId = (list: Member[]) => Object.fromEntries(list.map((m) => [m.id, m.display_name]));
    return data ? { current: byId(data.members), former: byId(data.former) } : { current: {}, former: {} };
  }, [data]);

  return (
    <CurrentUserIdContext.Provider value={currentUserId}>
      <MemberNamesContext.Provider value={names}>{children}</MemberNamesContext.Provider>
    </CurrentUserIdContext.Provider>
  );
}

interface MemberNameProps {
  // Null once the account is deleted: the server sends no id then.
  id: string | null;
  // The server's flag for an account that was deleted. It wins over any id,
  // so a deleted person is never shown as "someone" (an id we cannot name)
  // nor as "System" (no person at all).
  deleted?: boolean;
}

// MemberName renders a member by name ("you" for the signed-in user's own
// id). The id is deliberately not put in the tooltip: a person's id means
// nothing to a reader and is not theirs to hand out.
export default function MemberName({ id, deleted = false }: MemberNameProps) {
  const name = useMemberName(id ?? "");
  return <span>{deleted ? "Deleted user" : name}</span>;
}
