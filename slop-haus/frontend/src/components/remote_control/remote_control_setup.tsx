import { useState, type FormEvent } from "react";
import { Button } from "@slop-factory/ui";
import { apiClient, ApiError } from "@/api";
import Command from "@/shared/command/command";
import StepUpPrompt from "@/shared/step_up_prompt/step_up_prompt";
import { saveFile } from "@/components/agents_panel";
import { usePolledResource } from "@/lib/use_polled_resource";
import HelpSection from "./help_section";
import SupervisorRunSetup from "./supervisor_run_setup";
import "./remote_control_setup.scss";
import { errorMessage } from "@/lib/use_async_action";

// What was just downloaded. Held in memory only: the script is handed over
// when a remote control is registered or its workspaces are signed out, and is not
// kept anywhere to ask for again.
interface Issued {
  name: string;
  filename: string;
}

// A pending action that needs a recent sign-in first, to run again once the
// person has confirmed it.
type Retry = () => Promise<void>;

// How often what the person's supervisors run is read again: a start or a
// failure is reported by the supervisor, not by anything this page does.
const SUPERVISOR_RUN_REFRESH_MS = 10_000;

// RemoteControlSetup is where a person adds a remote control, closed until
// asked for like the Agents tab's "Add an agent". The main way is on a machine
// whose supervisor they signed in, which sets it up and starts it; running one
// by hand with a setup script is the fallback. A remote control belongs to
// whoever adds it: only they use it. Managing existing ones is in the
// sessions sidebar and the tenant's remote controls view.
//
// asView renders it as a view of the page's main pane rather than a closed
// section. onChanged is called after a registration, so the page's list of
// sessions can follow.
export default function RemoteControlSetup({ onChanged, asView = false }: { onChanged?: () => void; asView?: boolean }) {
  const [name, setName] = useState("");
  const [working, setWorking] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [issued, setIssued] = useState<Issued | null>(null);
  const [stepUp, setStepUp] = useState<Retry | null>(null);
  const own = usePolledResource(() => apiClient.listOwnRemoteControlSupervisors(), {
    intervalMs: SUPERVISOR_RUN_REFRESH_MS,
    fallbackError: "Could not load your machines.",
  });
  const canRunOnSupervisor = own.data !== undefined && own.data.provisionFromBoard && own.data.supervisors.length > 0;

  // run is every action here: a stale sign-in asks for confirmation and
  // then tries the same action again, anything else is shown.
  async function run(action: Retry) {
    setError(null);
    try {
      await action();
      setStepUp(null);
    } catch (err) {
      if (err instanceof ApiError && err.code === "reauth_required") {
        setStepUp(() => action);
      } else {
        setError(errorMessage(err, "Something went wrong."));
      }
    }
  }

  async function register(wanted: string) {
    setWorking(true);
    try {
      const { filename, script } = await apiClient.registerConductor(wanted);
      saveFile(filename, script);
      setIssued({ name: wanted, filename });
      setName("");
      onChanged?.();
    } finally {
      setWorking(false);
    }
  }

  function handleSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const wanted = name.trim();
    if (wanted === "") {
      setError("Give the remote control a name first.");
      return;
    }
    void run(() => register(wanted));
  }

  return (
    <HelpSection className="remote-control-setup" title="Add a remote control" asView={asView}>
      <div className="remote-control-setup__body">
        <p className="remote-control-setup__lede">
          A Remote Control is a Claude Code, pi or Codex session on one of your machines, which you drive from this page. Add
          it on a machine whose supervisor you signed in, and the supervisor sets it up and starts it. It is yours: only you
          can use it. Others in your team can see that it exists only if they own the team, and then they can switch it off
          but not use it.
        </p>

        {own.status === "loading" && own.data === undefined && <p role="status">Loading your machines…</p>}
        {own.error && own.data === undefined && (
          <p role="alert" className="remote-control-setup__notice">
            {own.error}
          </p>
        )}
        {own.data && (
          <SupervisorRunSetup
            own={own.data}
            run={run}
            onAdded={() => {
              void own.reload();
              onChanged?.();
            }}
          />
        )}

        {/* Open when there is no supervisor to run it: then this is the only way. */}
        <details className="remote-control-setup__by-hand" open={own.data !== undefined && !canRunOnSupervisor}>
          <summary>Run one yourself instead</summary>
          <p className="remote-control-setup__lede">
            For a machine with no supervisor, or to keep the session in your own terminal: register it here and run the setup
            script it downloads on that machine.
          </p>
          <form className="remote-control-setup__register" onSubmit={handleSubmit} aria-label="Register a remote control to run yourself">
            <label className="remote-control-setup__field">
              <span>Remote control name</span>
              <input type="text" value={name} placeholder="my-laptop" onChange={(e) => setName(e.target.value)} />
            </label>
            <Button variant="secondary" type="submit" disabled={working}>
              {working ? "Registering…" : "Register remote control"}
            </Button>
          </form>
          {issued && <IssuedScript issued={issued} onDismiss={() => setIssued(null)} />}
        </details>

        {error && (
          <p role="alert" className="remote-control-setup__notice">
            {error}
          </p>
        )}

        {stepUp && (
          <StepUpPrompt next={asView ? "/remote-control?view=setup" : "/remote-control"} onConfirmed={() => void run(stepUp)} onCancel={() => setStepUp(null)} />
        )}
      </div>
    </HelpSection>
  );
}

// IssuedScript says what to run, once. The script carries no credential: the
// workspace signs itself in by a code the owner approves on /device.
function IssuedScript({ issued, onDismiss }: { issued: Issued; onDismiss: () => void }) {
  const command = `bash ${issued.filename}`;
  return (
    <div className="remote-control-setup__issued" role="status">
      <p>
        Saved <code>{issued.filename}</code> for <strong>{issued.name}</strong>. Run it on the machine to drive. It
        carries no secret, and this is the only time it is offered: to get another, sign the remote control out for a
        new script.
      </p>
      <Command value={command} label="the setup command" />
      <Button variant="secondary" onClick={onDismiss}>
        Done
      </Button>
    </div>
  );
}
