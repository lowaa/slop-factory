// What each intake source setting does, in the server's own terms (see
// server/internal/intake). `hint` is the short line always shown under the
// control, so a touchscreen -- where nothing hovers -- still learns what
// matters; `tooltip` is the fuller explanation on hover or focus.

export interface FieldDoc {
  label: string;
  hint: string;
  tooltip: string;
}

export const FIELD_KEYS = [
  "name",
  "trust",
  "auth",
  "adapter",
  "reopen_policy",
  "resolve_closes",
  "default_status",
  "default_priority",
  "max_priority",
  "roles",
  "role_match",
  "paths",
] as const;

export type FieldKey = (typeof FIELD_KEYS)[number];

export const FIELD_DOCS: Record<FieldKey, FieldDoc> = {
  name: {
    label: "Name",
    hint: "Shown on its tickets and in the delivery list.",
    tooltip:
      'A label for this source. Its tickets are titled "[name] external report", and the delivery list names the source by it.',
  },
  trust: {
    label: "Trust",
    hint: "Untrusted tickets always start as Preparing.",
    tooltip:
      "Untrusted: every ticket is created Preparing, so a person reads it before any agent can claim it. Trusted: the source may file tickets as Open for agents to claim at once. Either way, what the sender wrote is shown as external content.",
  },
  auth: {
    label: "Credential type",
    hint: "How the sender proves it is this source.",
    tooltip:
      "bearer: the sender sends an Authorization: Bearer token. hmac: the sender signs each request body with a secret and sends the signature in the X-Slophaus-Signature header. The token or secret is shown once, after you create the source; Rotate replaces it.",
  },
  adapter: {
    label: "Payload format",
    hint: "Which JSON shape the sender posts.",
    tooltip:
      "native: slop-haus's own JSON (key, event, title, body, ...); an unknown field rejects the delivery. alertmanager: Alertmanager's webhook, one ticket per alert group, where firing opens and resolved resolves. Fixed once the source exists.",
  },
  reopen_policy: {
    label: "When a resolved ticket is reported again",
    hint: "Applies when an open arrives for a finished ticket.",
    tooltip:
      "A sender's open for a key whose ticket is already done or cancelled either files a new ticket, or returns that ticket to the queue. Updates and resolves only add a comment and never change this.",
  },
  resolve_closes: {
    label: "When the sender reports it resolved",
    hint: "Only the team's owner can turn this on, for a trusted source.",
    tooltip:
      "Off: a resolve only adds a comment. On: a resolve also cancels the ticket, but only while nobody has claimed it -- no agent has worked it and no one has commented. A ticket in progress, in review or discussed gets the comment alone. Cancelled, not done; a later open follows the reopen setting. To change a ticket's severity or fix version, the sender sends an update: it is added as a comment, never as the ticket's text.",
  },
  default_status: {
    label: "Default status",
    hint: "Used when the sender asks for no status.",
    tooltip:
      "The status a new ticket gets when the payload does not ask for one. An untrusted source is always Preparing, whatever is chosen or asked for.",
  },
  default_priority: {
    label: "Default priority",
    hint: "Used when the sender asks for no priority.",
    tooltip:
      "The priority a new ticket gets when the payload asks for none, or for one that does not exist.",
  },
  max_priority: {
    label: "Max priority",
    hint: "A sender can never ask for more than this.",
    tooltip:
      "The ceiling for the priority a sender asks for: a higher request is lowered to this one. It does not change the default.",
  },
  roles: {
    label: "Roles",
    hint: "Comma separated. Empty: any agent may claim.",
    tooltip:
      "Role names an agent must have to claim this source's tickets. Only a person sets them here; a payload never does. Leave empty and any agent may claim.",
  },
  role_match: {
    label: "Role match",
    hint: "How many of the roles an agent needs.",
    tooltip:
      "any: an agent with at least one of the roles may claim the ticket. all: it needs every role listed.",
  },
  paths: {
    label: "Paths",
    hint: "Comma separated repo paths, e.g. server/internal/.",
    tooltip:
      "Path scopes set on every ticket this source files. Where the tenant locks path scopes, they keep two agents out of the same files, as a ticket's scopes do anywhere else. Leave empty for none.",
  },
};
