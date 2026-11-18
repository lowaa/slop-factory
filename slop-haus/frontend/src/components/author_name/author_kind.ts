// Shared by the ticket views, which render a Comment's (author_agent_id,
// author_user_id) pair and used to treat "no agent id" as "must be the
// human" -- true back when the human was the only other kind of author.
// Now that comments carry a real author_user_id, that assumption is wrong:
// an agent, a signed-in person, a person whose account was deleted (the
// server then sends no id, only the flag), or (defensively) nobody at all.
export type AuthorKind = "agent" | "user" | "deleted" | "notice";

export function authorKind(agentId: string | null, userId: string | null, userDeleted = false): AuthorKind {
  if (agentId) return "agent";
  if (userId) return "user";
  if (userDeleted) return "deleted";
  return "notice";
}
