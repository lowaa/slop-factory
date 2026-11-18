import { useState, type FormEvent } from "react";
import { Button, Dropdown } from "@slop-factory/ui";
import { Link } from "@slop-factory/router";
import { apiClient, type DesiredAgentView, type OwnRemoteControlSupervisor, type OwnRemoteControlSupervisors } from "@/api";
import { agentStatus, HARNESS_OPTIONS, PI_WARNING, STATUS_LABELS } from "@/components/provisioning";
import { DEFAULT_HARNESS } from "@/domain/harnesses";
// Part of the setup panel: its form and list take that panel's styles.
import "./remote_control_setup.scss";

const WORKERS_PATH = "/workers";

// SupervisorRunSetup is the main way to add a remote control: a supervisor
// the person approved makes it, sets up its workspace and starts its host.
// run is the panel's action runner, which asks for a recent sign-in and
// tries again. Nothing is shown of supervisors someone else approved: a
// remote control one of those made would be its approver's, not this person's.
export default function SupervisorRunSetup({
  own,
  run,
  onAdded,
}: {
  own: OwnRemoteControlSupervisors;
  run: (action: () => Promise<void>) => Promise<void>;
  onAdded: () => void;
}) {
  if (!own.provisionFromBoard) {
    return (
      <p role="note">
        Your team does not provision agents from the board yet, so no supervisor can run a remote control for you. Its owner
        can turn that on; meanwhile, run one yourself below.
      </p>
    );
  }
  if (own.supervisors.length === 0) {
    return (
      <p role="note">
        None of your machines runs a supervisor you signed in. Add one on <Link to={WORKERS_PATH}>the Workers
        page</Link>, and it can run your remote controls; or run one yourself below.
      </p>
    );
  }
  return (
    <>
      <AddOnSupervisor supervisors={own.supervisors} run={run} onAdded={onAdded} />
      <SupervisorRunList supervisors={own.supervisors} />
    </>
  );
}

function AddOnSupervisor({
  supervisors,
  run,
  onAdded,
}: {
  supervisors: OwnRemoteControlSupervisor[];
  run: (action: () => Promise<void>) => Promise<void>;
  onAdded: () => void;
}) {
  const [name, setName] = useState("");
  const [supervisorId, setSupervisorId] = useState(supervisors[0].id);
  const [harness, setHarness] = useState<string>(DEFAULT_HARNESS);
  const [working, setWorking] = useState(false);
  const [invalid, setInvalid] = useState<string | null>(null);
  // A supervisor that went away since the list was read is not offered as chosen.
  const chosen = supervisors.some((s) => s.id === supervisorId) ? supervisorId : supervisors[0].id;

  function handleSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const wanted = name.trim();
    if (wanted === "") {
      setInvalid("Give the remote control a name first.");
      return;
    }
    setInvalid(null);
    void run(async () => {
      setWorking(true);
      try {
        await apiClient.addOwnRemoteControl(chosen, wanted, harness === DEFAULT_HARNESS ? undefined : harness);
        setName("");
        onAdded();
      } finally {
        setWorking(false);
      }
    });
  }

  return (
    <form className="remote-control-setup__register" onSubmit={handleSubmit} aria-label="Add a remote control on a machine">
      <label className="remote-control-setup__field">
        <span>Remote control name</span>
        <input type="text" value={name} placeholder="my-laptop" onChange={(e) => setName(e.target.value)} />
      </label>
      {supervisors.length > 1 ? (
        <Dropdown<string>
          label="Machine"
          value={chosen}
          options={supervisors.map((s) => ({ value: s.id, label: s.name }))}
          onChange={setSupervisorId}
        />
      ) : (
        <p className="remote-control-setup__machine">
          On <strong>{supervisors[0].name}</strong>
        </p>
      )}
      <Dropdown<string> label="Harness" value={harness} options={HARNESS_OPTIONS} onChange={setHarness} />
      <Button variant="primary" type="submit" disabled={working}>
        {working ? "Adding…" : "Add remote control"}
      </Button>
      {harness === "pi" && <p role="note">{PI_WARNING}</p>}
      {invalid && (
        <p role="alert" className="remote-control-setup__notice">
          {invalid}
        </p>
      )}
    </form>
  );
}

// SupervisorRunList is how each remote control a supervisor runs is coming
// along: until its host is up it is not among the machines to pick, so this
// is where a slow start or a failure shows.
function SupervisorRunList({ supervisors }: { supervisors: OwnRemoteControlSupervisor[] }) {
  const rows = supervisors.flatMap((s) => s.remote_controls.filter((r) => r.state !== "removed").map((r) => ({ supervisor: s.name, entry: r })));
  if (rows.length === 0) return null;
  return (
    <ul className="remote-control-setup__run-list" aria-label="Remote controls your supervisors run">
      {rows.map(({ supervisor, entry }) => (
        <li key={entry.id}>
          <strong>{entry.name}</strong> on {supervisor}: {statusText(entry)}
        </li>
      ))}
    </ul>
  );
}

function statusText(entry: DesiredAgentView): string {
  const status = agentStatus(entry);
  const label = STATUS_LABELS[status.kind];
  return "reason" in status && status.reason ? `${label}: ${status.reason}` : label;
}
