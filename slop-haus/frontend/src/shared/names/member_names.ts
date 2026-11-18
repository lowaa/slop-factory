import { createContext, useContext } from "react";

// The lookup MemberNamesProvider fills in, apart from the components --
// same split as agent_names, so a component file (member_name.tsx)
// exports only components for fast refresh.
export interface MemberNames {
  current: Record<string, string>;
  former: Record<string, string>;
}

export const MemberNamesContext = createContext<MemberNames>({ current: {}, former: {} });

// The signed-in user's own id, threaded down so their own comments
// read as "you" instead of their own name reflected back.
export const CurrentUserIdContext = createContext<string | null>(null);

// "someone" rather than a short id, unlike agent_names' shortId fallback:
// an agent's id is something an operator might actually go looking for in
// logs or a setup script, but a person's uuid means nothing to another
// person reading the board -- a vague "someone" is a more honest fallback
// than a string that looks like it should be meaningful and isn't.
export function useMemberName(id: string): string {
  const currentUserId = useContext(CurrentUserIdContext);
  const names = useContext(MemberNamesContext);
  if (id === currentUserId) return "you";
  if (names.current[id] !== undefined) return names.current[id];
  if (names.former[id] !== undefined) return `${names.former[id]} (former member)`;
  return "someone";
}
