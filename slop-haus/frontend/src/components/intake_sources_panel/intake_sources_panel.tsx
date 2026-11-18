import {
  useCallback,
  useEffect,
  useRef,
  useState,
} from "react";
import {
  apiClient,
  ApiError,
  type IntakeCredential,
  type IntakeSources,
} from "@/api";
import StepUpPrompt from "@/shared/step_up_prompt/step_up_prompt";
import CredentialBox from "./credential_box";
import DeliveryRow from "./delivery_row";
import NewSourceForm from "./new_source_form";
import SourceCard from "./source_card";
import { INITIAL_FORM, toRequest, type FormValues } from "./intake_form_values";
import "./intake_sources_panel.scss";
import { errorMessage } from "@/lib/use_async_action";

type SourcesState =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "loaded"; data: IntakeSources };

// What the step-up prompt resumes once the person has confirmed.
type PendingAction = { kind: "create" } | { kind: "rotate"; id: string };

export default function IntakeSourcesPanel() {
  const [state, setState] = useState<SourcesState>({ status: "loading" });
  const [form, setForm] = useState<FormValues>(INITIAL_FORM);
  const [busy, setBusy] = useState(false);
  const [actionError, setActionError] = useState<string | null>(null);
  const [credential, setCredential] = useState<IntakeCredential | null>(null);
  const [revokingId, setRevokingId] = useState<string | null>(null);
  const [revokeError, setRevokeError] = useState<string | null>(null);
  const [pending, setPending] = useState<PendingAction | null>(null);
  const [stepUp, setStepUp] = useState(false);

  // issued/applied guards a slower, older fetch from landing after a newer
  // one and putting back stale data -- same reasoning as members_panel.tsx.
  const issued = useRef(0);
  const applied = useRef(0);

  const fetchSources = useCallback(() => {
    const request = ++issued.current;
    return apiClient
      .listIntakeSources()
      .then((data) => {
        if (request < applied.current) return;
        applied.current = request;
        setState({ status: "loaded", data });
      })
      .catch((err: unknown) => {
        if (request < applied.current) return;
        applied.current = request;
        setState({
          status: "error",
          message:
            errorMessage(err, "Failed to load intake sources."),
        });
      });
  }, []);

  useEffect(() => {
    fetchSources();
  }, [fetchSources]);

  async function run(action: PendingAction) {
    setBusy(true);
    setActionError(null);
    try {
      const result =
        action.kind === "create"
          ? await apiClient.createIntakeSource(toRequest(form))
          : await apiClient.rotateIntakeSource(action.id);
      setCredential(result);
      if (action.kind === "create") setForm(INITIAL_FORM);
      await fetchSources();
    } catch (err) {
      if (err instanceof ApiError && err.code === "reauth_required") {
        setPending(action);
        setStepUp(true);
      } else {
        const fallback =
          action.kind === "create"
            ? "Failed to create source."
            : "Failed to rotate credential.";
        setActionError(errorMessage(err, fallback));
      }
    } finally {
      setBusy(false);
    }
  }

  function handleStepUpConfirmed() {
    const action = pending;
    setStepUp(false);
    setPending(null);
    if (action) void run(action);
  }

  function handleStepUpCancelled() {
    setStepUp(false);
    setPending(null);
  }

  async function handleRevoke(id: string) {
    setRevokeError(null);
    setRevokingId(id);
    try {
      await apiClient.revokeIntakeSource(id);
      await fetchSources();
    } catch (err) {
      setRevokeError(
        errorMessage(err, "Failed to revoke source."),
      );
    } finally {
      setRevokingId(null);
    }
  }

  const loaded = state.status === "loaded" ? state.data : null;
  const enabled = loaded ? loaded.enabled : true;
  const hmac = loaded ? loaded.hmac : true;
  const sourceNames = new Map(
    (loaded?.sources ?? []).map((source) => [source.id, source.name]),
  );

  function update<K extends keyof FormValues>(key: K, value: FormValues[K]) {
    setForm((current) => ({ ...current, [key]: value }));
  }

  return (
    <div className="intake-sources-panel">
      <section className="intake-sources-panel__section">
        <h2>Intake sources</h2>
        <div className="intake-sources-panel__instructions">
          <p>
            External systems -- Alertmanager, a support desk, any system that
            can make an HTTP request -- can open, update and resolve tickets by
            POSTing to a source's URL with its credential.
          </p>
          <p>
            An <strong>untrusted</strong> source's tickets are always created as
            Preparing, so a person reads them before any agent can claim one.
            What a sender says is shown as external content, never as the
            board's own words.
          </p>
          <p>
            The intake door is for the tailnet or cluster, not the internet.
          </p>
        </div>

        {!enabled && (
          <p
            className="intake-sources-panel__notice intake-sources-panel__notice--neutral"
            role="status"
          >
            The intake door is not configured on this server.
          </p>
        )}

        {actionError && (
          <p role="alert" className="intake-sources-panel__notice">
            {actionError}
          </p>
        )}

        {stepUp && (
          <StepUpPrompt
            next="/intake"
            onConfirmed={handleStepUpConfirmed}
            onCancel={handleStepUpCancelled}
          />
        )}

        {credential && (
          <CredentialBox
            credential={credential}
            onDone={() => setCredential(null)}
          />
        )}

        {!credential && !stepUp && (
          <NewSourceForm
            form={form}
            onChange={update}
            onSubmit={() => void run({ kind: "create" })}
            enabled={enabled}
            hmac={hmac}
            busy={busy}
          />
        )}
      </section>

      <section className="intake-sources-panel__section">
        <h3>Sources</h3>
        {revokeError && (
          <p role="alert" className="intake-sources-panel__notice">
            {revokeError}
          </p>
        )}
        {state.status === "loading" && <p>Loading…</p>}
        {state.status === "error" && (
          <p role="alert" className="intake-sources-panel__notice">
            {state.message}
          </p>
        )}
        {loaded && (
          <>
            <ul className="intake-sources-panel__list">
              {loaded.sources.map((source) => (
                <SourceCard
                  key={source.id}
                  source={source}
                  busy={busy}
                  revoking={revokingId === source.id}
                  onRotate={() => void run({ kind: "rotate", id: source.id })}
                  onRevoke={() => void handleRevoke(source.id)}
                />
              ))}
            </ul>
            {loaded.sources.length === 0 && (
              <p className="intake-sources-panel__empty">No sources yet.</p>
            )}
          </>
        )}
      </section>

      <section className="intake-sources-panel__section">
        <h3>Recent deliveries</h3>
        {loaded && (
          <>
            <ul className="intake-sources-panel__list">
              {loaded.deliveries.map((delivery) => (
                <DeliveryRow
                  key={delivery.id}
                  delivery={delivery}
                  sourceName={
                    sourceNames.get(delivery.source_id) ?? delivery.source_id
                  }
                />
              ))}
            </ul>
            {loaded.deliveries.length === 0 && (
              <p className="intake-sources-panel__empty">No deliveries yet.</p>
            )}
          </>
        )}
      </section>
    </div>
  );
}
