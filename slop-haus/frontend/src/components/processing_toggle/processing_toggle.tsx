import { useCallback, useEffect, useRef, useState } from "react";
import { apiClient, TICKET_PROCESSING_ENABLED } from "@/api";
import { announceProcessing, onProcessingAnnounced } from "./processing_events";
import "./processing_toggle.scss";
import { errorMessage } from "@/lib/use_async_action";

// The fleet's on/off switch, in the top bar because it is the one control
// that is about every screen rather than the one you are looking at.
//
// Off, agents that finish a ticket are allowed to stop instead of being
// sent back for the next one, and the claim path refuses outright -- so
// this is a pause on *starting* work, not a stop button. An agent already
// holding a ticket keeps being told to finish or release it, which is why
// the off state says "finishing current work" rather than "stopped".

type State =
  | { status: "loading" }
  | { status: "error"; message: string }
  // saving is a separate flag rather than a status of its own: the switch
  // keeps showing the value it is moving away from while the write is in
  // flight, so it does not flicker through a state nobody asked for.
  | { status: "ready"; enabled: boolean; saving: boolean };

export default function ProcessingToggle() {
  const [state, setState] = useState<State>({ status: "loading" });
  // A failed write is reported next to the switch rather than replacing
  // it: blanking the control the human just used is a poor way to report
  // a failure they might just retry.
  const [writeError, setWriteError] = useState<string | null>(null);

  const origin = useRef(Symbol("processing_toggle"));

  const load = useCallback(() => {
    return apiClient
      .getSetting(TICKET_PROCESSING_ENABLED)
      .then((setting) => {
        setState({ status: "ready", enabled: setting.bool_val ?? false, saving: false });
      })
      .catch((err: unknown) => {
        setState({
          status: "error",
          message: errorMessage(err, "Failed to read the processing switch."),
        });
      });
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  // The Tenant settings page writes this setting too.
  useEffect(
    () =>
      onProcessingAnnounced((enabled, from) => {
        if (from === origin.current) return;
        setState((current) =>
          current.status === "ready" ? { ...current, enabled } : current,
        );
      }),
    [],
  );

  async function toggle() {
    if (state.status !== "ready" || state.saving) return;

    const next = !state.enabled;
    setState({ ...state, saving: true });
    setWriteError(null);
    try {
      const written = await apiClient.updateSetting(TICKET_PROCESSING_ENABLED, {
        type: "bool",
        bool_val: next,
      });
      // The server's answer, not `next`: a write that was accepted but
      // landed differently should show what is actually stored.
      const stored = written.bool_val ?? next;
      setState({ status: "ready", enabled: stored, saving: false });
      announceProcessing(stored, origin.current);
    } catch (err: unknown) {
      // A 401 here means the session ended between page load and this
      // click -- the api client's own refresh-and-retry already ran and
      // failed, and onSignedOut is about to drop the whole app to the
      // login page, so there's nothing more specific worth saying than
      // the server's own message.
      const message = errorMessage(err, "Failed to change the processing switch.");
      setWriteError(message);
      // Back to the value we came from, since the write did not land.
      setState({ status: "ready", enabled: state.enabled, saving: false });
    }
  }

  if (state.status === "loading") {
    return <p className="processing-toggle processing-toggle--muted">Processing…</p>;
  }

  if (state.status === "error") {
    return (
      <p role="alert" className="processing-toggle processing-toggle--muted">
        {state.message}
      </p>
    );
  }

  const { enabled, saving } = state;
  return (
    <div className="processing-toggle">
      <button
        type="button"
        // role="switch" rather than a styled checkbox: this is a control
        // with two states and no form around it, and the role is what
        // makes aria-checked mean anything to a screen reader.
        role="switch"
        aria-checked={enabled}
        aria-label="Ticket processing"
        disabled={saving}
        className={`processing-toggle__switch processing-toggle__switch--${enabled ? "on" : "off"}`}
        onClick={toggle}
      >
        <span className="processing-toggle__track" aria-hidden="true">
          <span className="processing-toggle__thumb" />
        </span>
        <span className="processing-toggle__label">
          {enabled ? "Processing" : "Paused"}
        </span>
      </button>

      {!enabled && (
        <span className="processing-toggle__hint">
          agents finish current work, then stop
        </span>
      )}

      {writeError && (
        <span role="alert" className="processing-toggle__error">
          {writeError}
        </span>
      )}
    </div>
  );
}
