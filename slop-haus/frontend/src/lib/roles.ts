import type { RoleMatch, RoleRef } from "@/api";

// describeRequiredRoles is a ticket's role requirement in one short line --
// "role: deploy" for a single role, "roles: any of deploy, review" or
// "roles: all of deploy, review" once there's a choice to state. Shared by
// ticket_card's compact badge and ticket_detail's fuller display so the two
// never drift into describing the same requirement differently.
export function describeRequiredRoles(roles: RoleRef[], match: RoleMatch): string {
  if (roles.length === 0) return "";
  const names = roles.map((r) => r.name).join(", ");
  return roles.length === 1 ? `role: ${names}` : `roles: ${match} of ${names}`;
}

// NO_ELIGIBLE_AGENT_WARNING is the ticket-level "this cannot currently be
// claimed" notice -- the same wording everywhere it shows (ticket_card's
// compact badge, ticket_detail's banner) so a person sees one story about
// why a ticket is stuck, not two slightly different ones.
export const NO_ELIGIBLE_AGENT_WARNING =
  "No enabled agent has the roles this ticket needs, so no agent can claim it.";
