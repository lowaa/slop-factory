import { useEffect, useState } from "react";
import { apiClient, type PendingAuthorization } from "@/api";
import { AuthLayout, AuthNotice } from "@slop-factory/auth";
import { redirectBrowserTo } from "@/lib/navigation";
import { AgentPicker } from "@/components/agent_picker";
import { listPickableAgents, type PickableAgent } from "@/components/agent_picker";
import { useAgentChoice } from "@/components/agent_picker";
import { DecisionActions, type Decision } from "@/components/agent_picker";
import { DEFAULT_SLOT_CAP, parseSlotCap } from "@/domain/slot_cap";
import { AgentKindChoice, type NewAgentKind } from "./agent_kind_choice";
import { ReplaceAgentChoice } from "./replace_agent_choice";
import { listReplaceableAgents } from "./replaceable_agents";
import { captureAuthorizeRequestId, clearPendingAuthorizeRequest } from "./pending_authorize";
import "./authorize_page.scss";
import { errorMessage } from "@/lib/use_async_action";

// The consent page an OAuth client's authorization code flow lands a
// signed-in browser on (oauth.go's Authorize redirects here with
// "?request=<id>", see pending_authorize.ts for how that survives a detour
// through /login). Approving mints a grant for one of this tenant's
// agents -- existing or brand new -- and the decision either way ends with
// a real navigation to the client's own redirect URI, never a route this
// SPA owns.

type RequestState =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; pending: PendingAuthorization };

export default function AuthorizePage() {
  const [requestId] = useState<string | null>(captureAuthorizeRequestId);
  const [state, setState] = useState<RequestState>(
    requestId ? { status: "loading" } : { status: "error", message: "This authorization link is missing its request id." },
  );
  const [agents, setAgents] = useState<PickableAgent[]>([]);
  const agentChoice = useAgentChoice();
  const [newAgentKind, setNewAgentKind] = useState<NewAgentKind>("worker");
  const [slotCap, setSlotCap] = useState(String(DEFAULT_SLOT_CAP));
  const [replaceable, setReplaceable] = useState<PickableAgent[]>([]);
  const [replacing, setReplacing] = useState(false);
  const [replaceId, setReplaceId] = useState("");
  const [submitting, setSubmitting] = useState<Decision | null>(null);
  const [error, setError] = useState<string | null>(null);

  // The stash exists only to survive the trip through /login, and this
  // page is only ever shown signed in, so by now it has done its job. Left
  // set -- say the request had expired -- it would route every later
  // navigation back here (see app.tsx's screenFor).
  useEffect(() => {
    clearPendingAuthorizeRequest();
  }, []);

  useEffect(() => {
    if (!requestId) return;
    apiClient
      .getAuthRequest(requestId)
      .then((pending) => setState({ status: "ready", pending }))
      .catch((err: unknown) => {
        setState({
          status: "error",
          message: errorMessage(err, "Something went wrong. Please try again."),
        });
      });
  }, [requestId]);

  useEffect(() => {
    if (state.status !== "ready") return;
    // A failure here is not fatal -- "a new agent" still works without a
    // roster to pick from, so it is swallowed rather than replacing the
    // whole page with an error over a listing that isn't the main event.
    listPickableAgents()
      .then(setAgents)
      .catch(() => setAgents([]));
    listReplaceableAgents()
      .then(setReplaceable)
      .catch(() => setReplaceable([]));
  }, [state.status]);

  if (state.status === "loading") {
    return (
      <AuthLayout title="Authorize agent access">
        <p>Loading…</p>
      </AuthLayout>
    );
  }

  if (state.status === "error") {
    return (
      <AuthLayout title="Authorize agent access">
        <div className="auth-layout__body">
          <AuthNotice variant="error">{state.message}</AuthNotice>
        </div>
      </AuthLayout>
    );
  }

  const { pending } = state;
  // The kind only means something for a new agent; an existing one keeps its
  // own, and the server rejects the pair.
  function approval() {
    const base = agentChoice.approval();
    if (agentChoice.choice !== "new") return base;
    if (supervisor) return { ...base, agent_kind: newAgentKind, slot_cap: parseSlotCap(slotCap) ?? undefined };
    return { ...base, agent_kind: newAgentKind, ...(replacing ? { replaces_agent_id: replaceId } : {}) };
  }
  const newAgent = agentChoice.choice === "new";
  // Replacing is not offered to a supervisor: the server refuses the pair.
  const supervisor = newAgent && newAgentKind === "supervisor";
  const replaceComplete = !newAgent || supervisor || !replacing || replaceId !== "";
  const slotCapComplete = !supervisor || parseSlotCap(slotCap) !== null;
  async function decide(kind: Decision) {
    if (!requestId) return;
    setError(null);
    setSubmitting(kind);
    try {
      const redirectTo = await apiClient.decideAuthRequest(
        requestId,
        kind === "approve" ? approval() : { approve: false },
      );
      clearPendingAuthorizeRequest();
      redirectBrowserTo(redirectTo);
    } catch (err) {
      setError(errorMessage(err, "Something went wrong. Please try again."));
      setSubmitting(null);
    }
  }

  return (
    <AuthLayout title="Authorize agent access">
      <div className="auth-layout__body">
        <AuthNotice variant="info">
          <strong>{pending.client_name}</strong> wants to act as one of your agents.
        </AuthNotice>
        <p className="authorize-page__detail">
          If you approve, the approval is sent to a program on <strong>{pending.redirect_host}</strong> — that&rsquo;s
          on your own computer, not somewhere else on the internet.
        </p>

        <AgentPicker name="authorize-agent-choice" agents={agents} state={agentChoice} />
        {agentChoice.choice === "new" && (
          <AgentKindChoice name="authorize-agent-kind" kind={newAgentKind}
            onChange={setNewAgentKind}
            slotCap={slotCap}
            onSlotCap={setSlotCap}
          />
        )}

        {newAgent && !supervisor && (
          <ReplaceAgentChoice
            name="authorize-agent-replace"
            agents={replaceable}
            replacing={replacing}
            replaceId={replaceId}
            onReplacing={setReplacing}
            onReplaceId={setReplaceId}
          />
        )}

        {error && <AuthNotice variant="error">{error}</AuthNotice>}

        <DecisionActions canApprove={agentChoice.complete && replaceComplete && slotCapComplete} submitting={submitting} onDecide={(kind) => void decide(kind)} />

        <p className="authorize-page__headless-note">
          On a machine with no browser of its own: after you decide, this page tries to send your browser on to a
          program on your computer, at the address named above. If it lands on a page that fails to load, that is
          expected — copy the address from the bar and paste it back into the program that asked, e.g. the terminal
          running <code>claude mcp login &lt;server&gt; --no-browser</code>, or Claude Code&rsquo;s{" "}
          <code>/mcp</code> prompt.
        </p>
      </div>
    </AuthLayout>
  );
}
