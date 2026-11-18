import { TICKET_PROCESSING_ENABLED } from "@/api";
import { PROVISION_FROM_BOARD } from "@/components/provisioning";
import { REVIEW_PROVIDER_CONFIG } from "./review_provider_settings";

// Names are the server's setting keys. A setting the server returns that is
// not here is still listed, under its own name, so a new setting is usable
// before it is labelled.
export interface SettingLabel {
  label: string;
  description: string;
  // Present on an int setting edited in minutes; the server enforces the same
  // bounds.
  minutes?: { min: number; max: number };
  // Present on a unit-less int setting; the server enforces the same bounds.
  number?: { min: number; max: number };
  // The first occurrence of text in the description is shown as a link.
  link?: { text: string; to: string };
  // The server refuses a member's write; the row says so rather than invite it.
  ownerOnly?: boolean;
  // Asked before the switch is flipped to that value.
  confirm?: { on: SwitchConfirmation; off: SwitchConfirmation };
}

export interface SwitchConfirmation {
  lead: string;
  consequences?: string[];
  action: string;
}

export const TENANT_SETTING_LABELS: Record<string, SettingLabel> = {
  // Edited by its own section, not this list; the label serves history and
  // anywhere a setting is named.
  [REVIEW_PROVIDER_CONFIG]: {
    label: "Review provider",
    description:
      "Which provider, model and reasoning effort slop-review reviews with, and which failures fall back to a second provider. Empty means none set: each machine uses its own local settings.",
    ownerOnly: true,
  },
  [TICKET_PROCESSING_ENABLED]: {
    label: "Ticket processing",
    description:
      "When off, agents that finish a ticket may stop instead of being sent for the next one, and no new ticket is claimed. Work already held carries on. The switch in the header is this same setting.",
  },
  [PROVISION_FROM_BOARD]: {
    label: "Manage agents from the board",
    description:
      "Add, pause and remove each machine's agents on the Workers page, with no shell on the machine. While on, the slop-haus slot commands on your machines refuse and point here. Turning it off leaves every agent running; you then manage them on each machine.",
    link: { text: "Workers page", to: "/workers" },
    ownerOnly: true,
    confirm: {
      on: {
        lead: "Your machines' existing agents will be adopted as they are; from then on, add and remove agents here, not on the machines.",
        action: "Turn on",
      },
      off: {
        lead: "Turn off managing agents from the board?",
        consequences: [
          "Nothing stops: every agent keeps running.",
          "The board stops managing your machines' agents.",
          "You'll use slop-haus slot … on each machine instead.",
        ],
        action: "Turn off",
      },
    },
  },
  workspace_reconcile: {
    label: "Reconcile machines' workspaces",
    description:
      "Lets each machine's supervisor bring its agents' repo checkouts in line with the workspace manifest. Off by default; nothing on a machine changes while off.",
    ownerOnly: true,
  },
  "integration_lock.dead_holder_after": {
    label: "Flag a silent integration-lock holder after",
    description:
      "The board flags the integration lock's holder as dead once its agent has sent no heartbeat for this long. Nothing is released automatically: a person decides whether to release the lock.",
    minutes: { min: 5, max: 1440 },
  },
  "integration_lock.priority_aging_minutes": {
    label: "Integration-lock queue: move a waiter up one priority per",
    description:
      "The integration lock's queue serves urgent before high before normal before low, and a waiter moves up one step for every this many minutes it has waited, so a low-priority change is not starved. Urgent still goes next in line.",
    minutes: { min: 5, max: 240 },
  },
  merge_queue_batch_size: {
    label: "Merge queue: changes landed per CI run",
    description:
      "How many queued changes the server lands together with one CI run. 1 lands them one at a time. Only used when the merge method is merge queue. Batching (more than 1) also needs apps declared and 'Batch merges within an app' turned on in the workspace manifest.",
    number: { min: 1, max: 3 },
    ownerOnly: true,
  },
};

export interface SettingGroup {
  title: string;
  intro: string;
  names: Record<string, SettingLabel>;
}

const AUTO_APPROVE_INTRO =
  "When on, proposals of that kind from your own console and Remote Control sessions in this tenant are applied at once, as you. Other members' sessions, and agents with no owner, are never auto-approved. Turning one off affects only later proposals.";

export const YOUR_SETTING_GROUPS: SettingGroup[] = [
  {
    title: "Auto-approve your sessions' proposals",
    intro: AUTO_APPROVE_INTRO,
    names: {
      "approvals.auto.status": {
        label: "Parking or cancelling a ticket",
        description: "Proposals to park or cancel a ticket.",
      },
      "approvals.auto.edit": {
        label: "Editing a ticket's title, text, paths or priority",
        description: "Proposals to edit a ticket.",
      },
      "approvals.auto.ready": {
        label: "Moving a ticket out of preparing",
        description: "Proposals to make a preparing ticket ready.",
      },
      "approvals.auto.review_decision": {
        label: "Approving a ticket in review, or sending it back (approving leads to a merge)",
        description: "Proposals to approve or send back a ticket in review.",
      },
      "approvals.auto.manifest_edit": {
        label: "Editing the workspace manifest",
        description: "Proposals to edit the workspace manifest.",
      },
      "approvals.auto.discussion_answer": {
        label: "Answering a ticket in discussion",
        description: "Proposals to answer a ticket waiting on a human.",
      },
    },
  },
];

export const OTHER_GROUP_TITLE = "Other";

export function labelFor(name: string, labels: Record<string, SettingLabel>): SettingLabel {
  return labels[name] ?? { label: name, description: "" };
}
