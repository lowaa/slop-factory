import { useState, type ReactNode } from "react";
import { expiresIn } from "@/lib/format_time";
import { Button, Tooltip } from "@slop-factory/ui";
import {
  apiClient,
  type Agent,
  type AgentMessage,
  type AgentRole,
  type AgentSession,
  type HarnessConfigScope,
  type HarnessSettingDefinition,
  type HarnessSettings,
  type Grant,
  type Lease,
  type VariantVersion,
} from "@/api";
import { AgentAuthorizations } from "@/components/agent_authorizations";
import type { GrantsHandle } from "@/components/agent_authorizations";
import { AgentStateBadge, AgentLifecycleActions, AgentLifecycleFeedback, useAgentLifecycle } from "@/components/agent_lifecycle";
import AgentMessages from "./agent_messages";
import AgentRoleEditor from "./agent_role_editor/agent_role_editor";
import AgentVariantPicker from "./agent_variant_picker/agent_variant_picker";
import HarnessConfigForm from "@/shared/harness_config_form/harness_config_form";
import Timestamp from "@/shared/timestamp/timestamp";
import { summarise } from "@/domain/harness_labels";
import { saveFile } from "./save_file";
import { describeStewardReport } from "./steward_report";
import "./agents_panel.scss";
import { errorMessage } from "@/lib/use_async_action";

// leaseLabel turns a lease's expires_at into what the row shows -- "expires
// in Nm" while there's still time left, "expired" once it's in the past.
// An agent doesn't stop holding a ticket the instant its lease lapses (the
// server's reclaim sweep does that on its own schedule), so the human
// needs to be able to see a stale hold rather than have it silently read
// as still-active.
function leaseLabel(lease: Lease, now: Date): { text: string; expired: boolean } {
  const text = expiresIn(lease.expires_at, now.getTime(), "minute");
  return { text, expired: text === "expired" };
}

function LeaseHold({ lease }: { lease: Lease }) {
  const { text, expired } = leaseLabel(lease, new Date());
  return (
    <>
      #{lease.ticket_id}{" "}
      <span className={`agents-panel__expiry${expired ? " agents-panel__expiry--expired" : ""}`}>({text})</span>
    </>
  );
}

// sessionLabel is what the current run says it is, in one line: "pi 0.87.0
// · anthropic/claude-sonnet-4-5 · gpu=a100 · docker". Empty when it said
// nothing -- a run from before sessions described themselves, or a hook
// without python3.
function sessionLabel(session: AgentSession): string {
  const harness = [session.harness, session.harness_version].filter(Boolean).join(" ");
  const model = [session.provider, session.model].filter(Boolean).join("/");
  const labels = Object.entries(session.labels)
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([key, value]) => (value ? `${key}=${value}` : key));
  return [harness, model, ...labels].filter(Boolean).join(" · ");
}

// ConfigBadge says when the current run is not on the agent's current
// harness config: stale needs a restart to pick it up; unknown is a run
// that never said which it has (started without a launcher that reports it, or from an older
// workspace).
function ConfigBadge({ session, revision }: { session: AgentSession; revision: number }) {
  if (session.config_status === "stale") {
    return (
      <span
        className="agents-panel__config-badge agents-panel__config-badge--stale"
        title={`Started on config revision ${session.config_revision}; the current one is ${revision}. Restart it to apply.`}
      >
        stale config
      </span>
    );
  }
  if (session.config_status === "unknown") {
    return (
      <span
        className="agents-panel__config-badge"
        title="This run did not say which harness config it started with."
      >
        config unknown
      </span>
    );
  }
  return null;
}

// BundleBadge marks a run whose workspace has not run an update since the
// scripts the server hands out changed. Unknown (a workspace that predates the
// report) shows nothing: it is not known to be behind.
function BundleBadge({ session }: { session: AgentSession }) {
  if (session.bundle_status !== "stale") return null;
  return (
    <span
      className="agents-panel__config-badge agents-panel__config-badge--stale"
      title={`This workspace's scripts (bundle ${session.bundle_hash}) are not the ones the server serves now. Run .slop-haus/update.sh there.`}
    >
      stale scripts
    </span>
  );
}

// HarnessProps is what a row needs to show and edit its agent's override;
// null while the tenant's config is still loading.
export interface HarnessProps {
  definitions: HarnessSettingDefinition[];
  defaults: HarnessSettings | undefined;
  override: HarnessConfigScope | undefined;
}

function hasSettings(settings: HarnessSettings | undefined): boolean {
  return Object.values(settings ?? {}).some((s) => Object.keys(s).length > 0);
}

type NewScriptState =
  | { status: "idle" }
  | { status: "confirming" }
  | { status: "working" }
  | { status: "done"; filename: string }
  | { status: "error"; message: string };

// What the tenant offers a row to edit its agent with.
export interface AgentRowTenant {
  // Every role the tenant has, for the "Edit roles" checkbox list -- not
  // just this agent's own, which is agent.roles.
  allRoles: AgentRole[];
  harness: HarnessProps | null;
  // The variant versions an owner may put the agent on; null for anyone
  // else, who sees the agent's variant but cannot change it.
  variants: VariantVersion[] | null;
}

export interface AgentRowHandlers {
  // Disable, enable, remove or restore came back with the agent as it now is.
  onLifecycleChanged: (agent: Agent, partial: boolean) => void;
  // A new setup script signs out the agent's workspaces, so its grants are
  // stale once it succeeds.
  onSignedOut: () => void;
  onRolesChanged: (agent: Agent) => void;
  onHarnessConfigSaved: () => void;
  onVariantChanged: () => void;
  onMessageSent: () => void;
}

interface AgentRowProps {
  agent: Agent;
  tenant: AgentRowTenant;
  handlers: AgentRowHandlers;
  // The clients able to act as this agent, shown on its row.
  grants: Grant[];
  grantsHandle: GrantsHandle;
  // What the person has said to this agent lately, newest first.
  messages: AgentMessage[];
  // What the machine running this worker adds to its row: the state the
  // supervisor reports and the ways to pause, retire or re-harness it.
  machineControls?: ReactNode;
}

export default function AgentRow({ agent, tenant, handlers, grants, grantsHandle, messages, machineControls }: AgentRowProps) {
  const { allRoles, harness, variants } = tenant;
  const { onLifecycleChanged, onSignedOut, onRolesChanged, onHarnessConfigSaved, onVariantChanged, onMessageSent } =
    handlers;
  const [composing, setComposing] = useState(false);
  const [newScript, setNewScript] = useState<NewScriptState>({ status: "idle" });
  const [rolesOpen, setRolesOpen] = useState(false);
  const [configOpen, setConfigOpen] = useState(false);
  const [variantOpen, setVariantOpen] = useState(false);
  const overridden = hasSettings(harness?.override?.settings);
  // Saved but not in force: the agent runs on its variant's settings alone.
  const overrideInactive = overridden && agent.harness_override_inactive;

  async function saveOverride(settings: HarnessSettings) {
    await apiClient.saveAgentHarnessConfig(agent.id, settings);
    setConfigOpen(false);
    onHarnessConfigSaved();
  }
  const lifecycle = useAgentLifecycle(agent, onLifecycleChanged);
  const disabled = agent.disabled_at !== null;
  const runs = agent.session ? sessionLabel(agent.session) : "";

  async function handleConfirm() {
    setNewScript({ status: "working" });
    try {
      const { filename, script } = await apiClient.rotateAgentSetupScript(agent.id);
      saveFile(filename, script);
      setNewScript({ status: "done", filename });
    } catch (err) {
      setNewScript({
        status: "error",
        message: errorMessage(err, "Could not create a new setup script."),
      });
    } finally {
      // Even a failed request may have revoked before failing; a refetch
      // costs nothing and never shows a grant that is already gone.
      onSignedOut();
    }
  }

  // A disabled agent's new sign-in would be refused, so there is nothing
  // to offer it -- and a confirmation left open when it was disabled has
  // nothing left to confirm.
  const confirming = !disabled && (newScript.status === "confirming" || newScript.status === "working");

  const stewardReport = agent.kind === "steward" ? describeStewardReport(agent.observed) : null;

  return (
    <li className="agents-panel__row">
      <span className="agents-panel__who">
        <span className="agents-panel__name">{agent.name}</span>
        {agent.roles.length > 0 && (
          <span className="agents-panel__role-chips">
            {agent.roles.map((role) => (
              <span key={role.id} className="agents-panel__role-chip" title={role.description}>
                {role.name}
              </span>
            ))}
          </span>
        )}
        {agent.replaced_by && (
          <span className="agents-panel__replacement">replaced by {agent.replaced_by.name}</span>
        )}
        {agent.replaces && <span className="agents-panel__replacement">replaces {agent.replaces.name}</span>}
        {runs && (
          <span className="agents-panel__runs" title={runs}>
            {runs}
          </span>
        )}
        {(agent.session || overridden || agent.variant) && (
          <span className="agents-panel__config-chips">
            {agent.session && <ConfigBadge session={agent.session} revision={agent.config_revision} />}
            {agent.session && <BundleBadge session={agent.session} />}
            {agent.variant && (
              <span
                className="agents-panel__config-badge agents-panel__config-badge--variant"
                title={`Runs on ${agent.variant.name} v${agent.variant.version}'s settings alone (${agent.variant.harness}).`}
              >
                variant {agent.variant.name} v{agent.variant.version}
              </span>
            )}
            {overridden && (
              <span
                className={`agents-panel__config-badge${overrideInactive ? " agents-panel__config-badge--inactive" : ""}`}
                title={
                  overrideInactive
                    ? "This agent's harness config override is inactive while it is on a variant."
                    : "This agent has its own harness config override."
                }
              >
                config override{overrideInactive ? " (inactive)" : ""}
              </span>
            )}
          </span>
        )}
      </span>
      <AgentStateBadge agent={agent} />
      <span className="agents-panel__holds">{agent.lease ? <LeaseHold lease={agent.lease} /> : "idle"}</span>
      <span className="agents-panel__actions">
        {/* A steward is minted by a supervisor and takes no setup script, messages
            or roles; a person can set its harness config, switch it off or put it away. */}
        {agent.kind !== "steward" && (
          <>
            {!disabled && (
              <Tooltip text="Create a new setup script for this agent. Downloading it signs out every workspace running as it.">
                <Button size="sm" onClick={() => setNewScript({ status: "confirming" })} disabled={confirming}>
                  New setup script
                </Button>
              </Tooltip>
            )}
            {!disabled && (
              <Tooltip text="Send this agent a message. It arrives with the agent's next call to slop-haus.">
                <Button size="sm" onClick={() => setComposing(true)} disabled={composing}>
                  Message
                </Button>
              </Tooltip>
            )}
            <Tooltip text="Choose which roles this agent has. Roles decide which tickets it may claim.">
              <Button size="sm" onClick={() => setRolesOpen(true)} disabled={rolesOpen}>
                Edit roles
              </Button>
            </Tooltip>
            {variants && (
              <Tooltip text="Put this agent on a variant: it then runs on that version's settings and fleet instructions alone.">
                <Button
                  size="sm"
                  onClick={() => setVariantOpen(true)}
                  disabled={variantOpen}
                >
                  Variant
                </Button>
              </Tooltip>
            )}
          </>
        )}
        {harness && (
          <Tooltip text="Override the default harness settings for this agent alone. Applies at its next start.">
            <Button size="sm" onClick={() => setConfigOpen(true)} disabled={configOpen}>
              Harness config
            </Button>
          </Tooltip>
        )}
        <AgentLifecycleActions lifecycle={lifecycle} />
      </span>
      {machineControls && <div className="agents-panel__machine-controls">{machineControls}</div>}

      {agent.session?.stuck && (
        <p className="agents-panel__row-note agents-panel__row-note--error" role="status">
          Stuck since <Timestamp at={agent.session.stuck.at} />: {agent.session.stuck.reason}
        </p>
      )}

      <AgentMessages
        agentId={agent.id}
        agentName={agent.name}
        messages={messages}
        heldTicketId={agent.lease?.ticket_id ?? null}
        composing={composing && !disabled}
        onClose={() => setComposing(false)}
        onSent={onMessageSent}
      />

      {confirming && (
        <div className="agents-panel__confirm" role="group" aria-label={`Confirm new setup script for ${agent.name}`}>
          <p>
            This signs out every workspace running as <strong>{agent.name}</strong> right away. Nothing can act as it
            again until you run the new script and approve its sign-in.
          </p>
          <div className="agents-panel__confirm-actions">
            <Button size="sm" variant="danger" onClick={handleConfirm} disabled={newScript.status === "working"}>
              {newScript.status === "working" ? "Signing out…" : "Sign out and download"}
            </Button>
            <Button
              size="sm"
              variant="ghost"
              onClick={() => setNewScript({ status: "idle" })}
              disabled={newScript.status === "working"}
            >
              Cancel
            </Button>
          </div>
        </div>
      )}
      {newScript.status === "done" && (
        <p className="agents-panel__row-note" role="status">
          Saved <code>{newScript.filename}</code>. Run it in {agent.name}&rsquo;s checkout and approve the sign-in it
          starts.
        </p>
      )}
      {newScript.status === "error" && (
        <p className="agents-panel__row-note agents-panel__row-note--error" role="alert">
          {newScript.message}
        </p>
      )}

      {overrideInactive && harness && agent.variant && (
        <div className="agents-panel__override-inactive" role="note">
          <p>
            Inactive override: {agent.name} runs on {agent.variant.name} v{agent.variant.version}&rsquo;s settings
            alone, so the override below is kept but not applied. It applies again when the agent leaves the variant.
          </p>
          <ul className="agents-panel__config-summary">
            {summarise(harness.definitions, harness.override?.settings).map((line) => (
              <li key={line}>{line}</li>
            ))}
          </ul>
        </div>
      )}

      {variants && variantOpen && (
        <AgentVariantPicker
          agent={agent}
          variants={variants}
          onChanged={onVariantChanged}
          onClose={() => setVariantOpen(false)}
        />
      )}

      {configOpen && harness && (
        <div className="agents-panel__config-editor">
          <p>
            {agent.name}&rsquo;s own settings, laid over the defaults one at a time. Leave a field empty to take
            the default. Applies at its next start.
          </p>
          <HarnessConfigForm
            definitions={harness.definitions}
            saved={harness.override?.settings}
            inherited={harness.defaults}
            onSave={saveOverride}
            onCancel={() => setConfigOpen(false)}
            label={`Harness config for ${agent.name}`}
          />
        </div>
      )}

      {rolesOpen && (
        <AgentRoleEditor
          agent={agent}
          allRoles={allRoles}
          onRolesChanged={onRolesChanged}
          onClose={() => setRolesOpen(false)}
        />
      )}
      {stewardReport && (
        <p
          className={`agents-panel__steward-report${agent.observed?.phase === "failed" ? " agents-panel__steward-report--failed" : ""}`}
          role="status"
        >
          {stewardReport}
        </p>
      )}
      <div className="agents-panel__row-notice">
        <AgentAuthorizations grants={grants} handle={grantsHandle} />
      </div>
      <AgentLifecycleFeedback lifecycle={lifecycle} />
    </li>
  );
}
