import { useEffect, useState, type FormEvent } from "react";
import { apiClient, type PendingDeviceAuthorization } from "@/api";
import { AuthLayout, AuthNotice } from "@slop-factory/auth";
import { Button } from "@slop-factory/ui";
import { AgentPicker } from "@/components/agent_picker";
import { listPickableAgents, type PickableAgent } from "@/components/agent_picker";
import { useAgentChoice } from "@/components/agent_picker";
import { DecisionActions, type Decision } from "@/components/agent_picker";
import Timestamp from "@/shared/timestamp/timestamp";
import { AgentKindChoice, type NewAgentKind } from "@/components/authorize_page";
import { DEFAULT_SLOT_CAP, parseSlotCap } from "@/domain/slot_cap";
import { captureDeviceParams, clearPendingDevice } from "./pending_device";
import "./device_page.scss";
import { errorMessage } from "@/lib/use_async_action";

// Where a workspace's device flow (RFC 8628, walked by
// server/internal/workspace/assets/sh/token.sh's cmd_login) sends a human to approve
// it. token.sh prints the verification URL with "?code=" already filled in
// and, when it knows the agent's own name, "&agent=" too -- both optional,
// since the same page also serves someone who was just given a bare code to
// type in by hand.

type Stage =
  | { kind: "code" }
  | { kind: "ready"; pending: PendingDeviceAuthorization }
  | { kind: "done"; approved: boolean };

export default function DevicePage() {
  const [captured] = useState(captureDeviceParams);
  const [userCode, setUserCode] = useState(captured.code ?? "");
  const [stage, setStage] = useState<Stage>({ kind: "code" });
  const [lookingUp, setLookingUp] = useState(false);
  const [lookupError, setLookupError] = useState<string | null>(null);
  const [agents, setAgents] = useState<PickableAgent[]>([]);
  const agentChoice = useAgentChoice(captured.agent ?? "");
  const { setChoice, setAgentId } = agentChoice;
  const requested = captured.supervisor;
  // The hint only preselects; what is granted is whatever these hold when the
  // human approves, and that is what the form shows.
  const [newAgentKind, setNewAgentKind] = useState<NewAgentKind>(requested ? "supervisor" : "worker");
  const [slotCap, setSlotCap] = useState(String(requested?.slots ?? DEFAULT_SLOT_CAP));
  const [submitting, setSubmitting] = useState<Decision | null>(null);
  const [decisionError, setDecisionError] = useState<string | null>(null);

  async function lookup(code: string) {
    setLookupError(null);
    setLookingUp(true);
    try {
      const pending = await apiClient.lookupDevice(code);
      setStage({ kind: "ready", pending });
    } catch (err) {
      setLookupError(errorMessage(err, "Something went wrong. Please try again."));
    } finally {
      setLookingUp(false);
    }
  }

  // As on the consent page: the stash only has to outlive the trip through
  // /login, and left set it would route every later navigation back here.
  useEffect(() => {
    clearPendingDevice();
  }, []);

  // A code arriving via the URL (the link token.sh prints) is looked up
  // right away -- someone who followed that link should not also have to
  // press a button to submit the code it already carried.
  useEffect(() => {
    if (captured.code) void lookup(captured.code);
    // captured is fixed for this component's lifetime (see the useState
    // initializer above), so this runs exactly once.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => {
    if (stage.kind !== "ready") return;
    listPickableAgents()
      .then((enabled) => {
        setAgents(enabled);
        // ?agent=<name> preselects that agent if it's a real, enabled one --
        // otherwise it's a suggestion for the *new* agent's name, which
        // agentName is already initialized to.
        // A supervisor is always a new agent, so a request for one keeps the
        // preselection off an existing agent that happens to share the name.
        if (captured.agent && !requested) {
          const match = enabled.find((a) => a.name === captured.agent);
          if (match) {
            setChoice("existing");
            setAgentId(match.id);
          }
        }
      })
      .catch(() => setAgents([]));
  }, [stage.kind, captured.agent, requested, setChoice, setAgentId]);

  function handleSubmitCode(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    void lookup(userCode.trim());
  }

  const newAgent = agentChoice.choice === "new";
  const supervisor = newAgent && newAgentKind === "supervisor";
  const slotCapComplete = !supervisor || parseSlotCap(slotCap) !== null;
  function approval() {
    const base = agentChoice.approval();
    if (!newAgent) return base;
    if (supervisor) return { ...base, agent_kind: newAgentKind, slot_cap: parseSlotCap(slotCap) ?? undefined };
    return { ...base, agent_kind: newAgentKind };
  }

  async function decide(kind: Decision) {
    if (stage.kind !== "ready") return;
    setDecisionError(null);
    setSubmitting(kind);
    try {
      await apiClient.decideDevice(
        stage.pending.user_code,
        kind === "approve" ? approval() : { approve: false },
      );
      clearPendingDevice();
      setStage({ kind: "done", approved: kind === "approve" });
    } catch (err) {
      setDecisionError(errorMessage(err, "Something went wrong. Please try again."));
      setSubmitting(null);
    }
  }

  if (stage.kind === "done") {
    return (
      <AuthLayout title="Device sign-in">
        <div className="auth-layout__body">
          <AuthNotice variant={stage.approved ? "success" : "info"}>
            {stage.approved ? "Approved. " : "Denied. "}Done — return to your terminal.
          </AuthNotice>
        </div>
      </AuthLayout>
    );
  }

  if (stage.kind === "code") {
    return (
      <AuthLayout title="Device sign-in">
        <form className="auth-layout__form" onSubmit={handleSubmitCode}>
          <label className="auth-layout__field">
            <span>Device code</span>
            <input
              type="text"
              placeholder="XXXX-XXXX"
              value={userCode}
              onChange={(e) => setUserCode(e.target.value)}
              required
            />
          </label>
          {lookupError && <AuthNotice variant="error">{lookupError}</AuthNotice>}
          <Button
            type="submit"
            variant="primary"
            className="auth-layout__submit"
            disabled={lookingUp || userCode.trim() === ""}
          >
            {lookingUp ? "Looking up…" : "Look up"}
          </Button>
        </form>
      </AuthLayout>
    );
  }

  const { pending } = stage;
  return (
    <AuthLayout title="Device sign-in">
      <div className="auth-layout__body">
        <AuthNotice variant="info">
          <strong>{pending.client_name}</strong> wants to sign in as one of your agents, using code{" "}
          <code>{pending.user_code}</code>.
        </AuthNotice>

        <StartedFrom pending={pending} />

        {requested && (
          <AuthNotice variant="info">
            It asked to be authorized as a supervisor that may run up to {requested.slots} worker
            {requested.slots === 1 ? "" : "s"} at once. That is only its request: what you approve is what you
            select below.
          </AuthNotice>
        )}

        <AgentPicker name="device-agent-choice" agents={agents} state={agentChoice} />
        {newAgent && (
          <AgentKindChoice name="device-agent-kind" kind={newAgentKind} onChange={setNewAgentKind}
            slotCap={slotCap} onSlotCap={setSlotCap} />
        )}

        {decisionError && <AuthNotice variant="error">{decisionError}</AuthNotice>}

        <DecisionActions canApprove={agentChoice.complete && slotCapComplete} submitting={submitting} onDecide={(kind) => void decide(kind)} />
      </div>
    </AuthLayout>
  );
}

// Anyone can start a device sign-in and send its code to a member to
// approve (RFC 8628 section 5.4): approving hands the sender an agent
// token. Where the sign-in came from is the one thing the page can show
// that such a sender can't choose, so it is shown every time, with a
// warning when it isn't this browser's own network.
function StartedFrom({ pending }: { pending: PendingDeviceAuthorization }) {
  const origin = (
    <>
      Started {pending.started_from ? <>from <code>{pending.started_from}</code> </> : null}at{" "}
      <Timestamp at={pending.started_at} />.
    </>
  );
  if (pending.from_this_network) {
    return <p className="device-page__origin">{origin}</p>;
  }
  // No address to compare (a code from before origins were recorded): no
  // evidence either way, so no warning, but the same advice.
  if (!pending.started_from) {
    return <p className="device-page__origin">{origin} Only approve a code you started yourself, just now.</p>;
  }
  return (
    <AuthNotice variant="error">
      {origin} That isn’t this browser’s network, which is expected if you ran <code>slop setup-workspace</code> on
      another machine. Only approve a code you started yourself, just now. If someone sent you this code or link,
      deny it: approving would give them an agent in your tenant.
    </AuthNotice>
  );
}
