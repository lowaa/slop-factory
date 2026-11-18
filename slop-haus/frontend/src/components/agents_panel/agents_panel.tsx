import { useCallback, useEffect, useState, type ReactNode } from "react";
import { Link } from "@slop-factory/router";
import { apiClient, type Agent, type AgentMessage, type Grant, type VariantVersion } from "@/api";
import { liveKeys, useLiveInvalidate } from "@/lib/live";
import { useResource } from "@/lib/resource";
import { MachineRoster } from "@/components/provisioning";
import { OtherAuthorizations } from "@/components/agent_authorizations";
import { grantsOf, useGrants } from "@/components/agent_authorizations";
import { ShowRemovedToggle } from "@/components/agent_lifecycle";
import AgentRow from "./agent_row";
import AgentRoles, { type RolesState } from "./agent_roles";
import SupervisorOfflineBanner from "./supervisor_offline_banner";
import SupervisorSlots from "./supervisor_slots";
import HarnessConfigSection, { type HarnessConfigState } from "./harness_config_section";
import "./agents_panel.scss";
import { errorMessage } from "@/lib/use_async_action";

const SUPERVISOR_POLL_MS = 60_000;
const LIVE_KEYS = [liveKeys.agentMessages(), liveKeys.supervisorStatus()];
const NO_MESSAGES: AgentMessage[] = [];

type AgentsState =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "loaded"; agents: Agent[] };

// unlinkedGrants are the grants whose agent has no row on this page. The
// Workers page lists workers and sessions; a remote control's grants show on
// its own page, and a supervisor has no row of its own anywhere.
function unlinkedGrants(grants: Grant[], roster: Agent[]): Grant[] {
  const known = new Set(roster.map((a) => a.id));
  return grants.filter((g) => g.agent_kind !== "conductor" && !known.has(g.agent_id));
}

export default function AgentsPanel() {
  const [state, setState] = useState<AgentsState>({ status: "loading" });
  // Not remembered across visits: a roster that silently opens with rows
  // missing is worse than one that asks for a click to tidy it.
  const [hideDisabled, setHideDisabled] = useState(false);
  // A Remove that stopped after the disable: the row stays through "Hide
  // disabled" so its error and retry don't vanish with it.
  const [halfRemovedId, setHalfRemovedId] = useState<string | null>(null);
  const [showRemoved, setShowRemoved] = useState(false);

  const grantsHandle = useGrants();
  const { refresh: fetchGrants } = grantsHandle;

  // Newest first, across agents; each row takes its own.

  const [rolesState, setRolesState] = useState<RolesState>({ status: "loading" });
  const [harnessState, setHarnessState] = useState<HarnessConfigState>({ status: "loading" });

  const fetchAgents = useCallback(() => {
    return apiClient
      // Archived ones too: the panel hides them itself.
      .listAgents({ includeArchived: true })
      .then((agents) => setState({ status: "loaded", agents }))
      .catch((err: unknown) => {
        setState({ status: "error", message: errorMessage(err, "Failed to load workers.") });
      });
  }, []);

  const fetchRoles = useCallback(() => {
    return apiClient
      .listAgentRoles()
      .then((roles) => setRolesState({ status: "loaded", roles }))
      .catch((err: unknown) => {
        setRolesState({
          status: "error",
          message: errorMessage(err, "Failed to load agent roles."),
        });
      });
  }, []);

  // null for anyone but an owner: the server refuses them the list, and
  // that is all the picker needs to know about who may use it.
  const [variants, setVariants] = useState<VariantVersion[] | null>(null);
  const fetchVariants = useCallback(() => {
    return apiClient
      .listVariants()
      .then(setVariants)
      .catch(() => setVariants(null));
  }, []);

  const fetchHarnessConfig = useCallback(() => {
    return apiClient
      .getHarnessConfig()
      .then((config) => setHarnessState({ status: "loaded", config }))
      .catch((err: unknown) => {
        setHarnessState({
          status: "error",
          message: errorMessage(err, "Failed to load the harness config."),
        });
      });
  }, []);

  // A failed refresh keeps what is shown: the state of a message is not worth
  // an error banner, and the next refresh corrects it. Delivery happens on the
  // agent's side, with nothing on this screen to trigger a refresh. A message
  // turning delivered moves the board revision (the board_revision trigger on agent_messages), so it is
  // followed over the board stream rather than polled for: a poll's phase
  // decides how late pending turns delivered.
  const messagesResource = useResource(liveKeys.agentMessages(), () => apiClient.listAgentMessages());
  // Newest first, across agents; each row takes its own.
  const messages = messagesResource.data ?? NO_MESSAGES;
  const fetchMessages = messagesResource.reload;

  // A failed fetch shows nothing rather than an error: a banner that says the
  // supervisor is down must not appear on the strength of a failed request,
  // and the next refresh corrects it. Polled as well as followed over the
  // board stream: offline is a function of the clock, so the quiet board that
  // a dead supervisor leaves behind sends no event to say so.
  const supervisor = useResource(liveKeys.supervisorStatus(), () => apiClient.getSupervisorStatus(), {
    pollMs: SUPERVISOR_POLL_MS,
  }).data;

  useLiveInvalidate(LIVE_KEYS);

  useEffect(() => {
    fetchAgents();
    fetchRoles();
    fetchHarnessConfig();
    fetchVariants();
  }, [fetchAgents, fetchRoles, fetchHarnessConfig, fetchVariants]);

  // A save moves the revision the roster marks sessions against, so both
  // are refetched rather than patched.
  const handleHarnessConfigSaved = useCallback(() => {
    fetchHarnessConfig();
    fetchAgents();
  }, [fetchHarnessConfig, fetchAgents]);

  // A role's name/description feeds every row's chips, and deleting one
  // drops it from whichever agents held it -- either way the agent list
  // this component already holds is stale, so both are refetched together
  // rather than trying to patch chips in from just the roles response.
  const handleRolesChanged = useCallback(() => {
    fetchRoles();
    fetchAgents();
  }, [fetchRoles, fetchAgents]);

  function handleAgentRolesChanged(updated: Agent) {
    setState((prev) =>
      prev.status === "loaded"
        ? { status: "loaded", agents: prev.agents.map((a) => (a.id === updated.id ? updated : a)) }
        : prev,
    );
  }

  const everyAgent = state.status === "loaded" ? state.agents : [];
  const removedAgents = everyAgent.filter((a) => a.archived_at !== null && a.kind !== "steward");
  const loadedAgents = everyAgent.filter((a) => a.archived_at === null);
  // The roster is workers only; a console is the person's own session and
  // has its own section, so it never counts toward the worker roster.
  // The steward is minted by a supervisor, not set up here; it is listed so a
  // person can switch it off or put it away.
  const isSession = (a: Agent) => a.kind === "console";
  const isSteward = (a: Agent) => a.kind === "steward";
  const workers = loadedAgents.filter((a) => !isSession(a) && !isSteward(a));
  const consoles = loadedAgents.filter(isSession);
  const stewards = everyAgent.filter(isSteward);
  const visibleAgents = hideDisabled ? workers.filter((a) => a.disabled_at === null || a.id === halfRemovedId) : workers;

  function replaceAgent(updated: Agent) {
    setState((prev) =>
      prev.status === "loaded"
        ? { status: "loaded", agents: prev.agents.map((a) => (a.id === updated.id ? updated : a)) }
        : prev,
    );
  }

  // One row per agent, whichever list it sits in.
  function renderRow(agent: Agent, machineControls?: ReactNode) {
    return (
      <AgentRow
        key={agent.id}
        agent={agent}
        tenant={{
          allRoles: rolesState.status === "loaded" ? rolesState.roles : [],
          harness:
            harnessState.status === "loaded"
              ? {
                  definitions: harnessState.config.definitions,
                  defaults: harnessState.config.defaults?.settings,
                  override: harnessState.config.overrides.find((o) => o.agent_id === agent.id),
                }
              : null,
          variants,
        }}
        handlers={{
          onLifecycleChanged: (a, partial) => {
            replaceAgent(a);
            setHalfRemovedId(partial ? a.id : null);
          },
          onSignedOut: () => void fetchGrants(),
          onRolesChanged: handleAgentRolesChanged,
          onHarnessConfigSaved: handleHarnessConfigSaved,
          onVariantChanged: handleHarnessConfigSaved,
          onMessageSent: () => void fetchMessages(),
        }}
        grants={grantsOf(grantsHandle, agent.id)}
        grantsHandle={grantsHandle}
        messages={messages.filter((m) => m.agent_id === agent.id)}
        machineControls={machineControls}
      />
    );
  }

  return (
    <div className="agents-panel">
      {supervisor?.offline && <SupervisorOfflineBanner status={supervisor} />}
      {supervisor && <SupervisorSlots slots={supervisor.slots} />}

      {/* Ahead of the roster: the defaults are what each row's per-agent
          override builds on. */}
      <HarnessConfigSection state={harnessState} onSaved={handleHarnessConfigSaved} />

      {state.status === "loading" && <p>Loading workers…</p>}
      {state.status === "error" && (
        <p role="alert" className="agents-panel__notice">
          {state.message}
        </p>
      )}
      {state.status === "loaded" && (
        // A header and a grid, not a row of floating pills: this is the
        // screen for answering "which one has gone quiet", and that is a
        // question you answer by scanning a column.
        <section className="agents-panel__roster">
          <label className="agents-panel__filter">
            <input type="checkbox" checked={hideDisabled} onChange={(e) => setHideDisabled(e.target.checked)} />
            Hide disabled
          </label>
          <div className="agents-panel__head" aria-hidden="true">
            <span>Worker</span>
            <span>State</span>
            <span>Holds</span>
            <span />
          </div>
          <MachineRoster
            workers={visibleAgents}
            allWorkers={workers}
            status={supervisor}
            renderList={(agents, controlsFor) => (
              <ul className="agents-panel__list">{agents.map((a) => renderRow(a, controlsFor?.(a)))}</ul>
            )}
          />
          <ShowRemovedToggle count={removedAgents.length} checked={showRemoved} onChange={setShowRemoved} />
          {showRemoved && removedAgents.length > 0 && (
            <ul className="agents-panel__list agents-panel__list--removed" aria-label="Removed agents">
              {removedAgents.map((a) => renderRow(a))}
            </ul>
          )}
          {workers.length === 0 && <p className="agents-panel__empty">No workers yet.</p>}
          {workers.length > 0 && visibleAgents.length === 0 && (
            <p className="agents-panel__empty">Every worker is disabled.</p>
          )}
        </section>
      )}

      {state.status === "loaded" && (
        <section className="agents-panel__section" aria-labelledby="agents-panel-sessions">
          <h2 id="agents-panel-sessions">Your sessions</h2>
          {consoles.length === 0 ? (
            <p className="agents-panel__empty">No sessions yet.</p>
          ) : (
            <ul className="agents-panel__list">{consoles.map((a) => renderRow(a))}</ul>
          )}
        </section>
      )}

      {stewards.length > 0 && (
        <section className="agents-panel__section" aria-labelledby="agents-panel-steward">
          <h2 id="agents-panel-steward">Steward</h2>
          <ul className="agents-panel__list">{stewards.map((a) => renderRow(a))}</ul>
        </section>
      )}

      <AgentRoles state={rolesState} onChanged={handleRolesChanged} />

      {state.status === "loaded" && grantsHandle.state.status === "loaded" && (
        <OtherAuthorizations grants={unlinkedGrants(grantsHandle.state.grants, state.agents)} handle={grantsHandle} />
      )}

      <p>
        <Link to="/workers/add-machine">Add a machine</Link>: download, run and start a supervisor on another computer.
      </p>
    </div>
  );
}
