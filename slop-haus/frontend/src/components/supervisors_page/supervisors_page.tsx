import { useCallback, useEffect, useRef, useState } from "react";
import { Link } from "@slop-factory/router";
import { apiClient, type SupervisorStatus } from "@/api";
import Command from "@/shared/command/command";
import AddSlotManually from "./add_slot_manually";
import "./supervisors_page.scss";

const SUPERVISOR_POLL_MS = 60_000;
const SETUP_SCRIPT_URL = "/api/supervisor/setup_script";
const SETUP_SCRIPT_FILENAME = "slop-haus-supervisor-setup.sh";

export default function SupervisorsPage() {
  // null until the first answer: a failed request must not read as "no
  // supervisor yet".
  const [status, setStatus] = useState<SupervisorStatus | null>(null);
  const request = useRef(0);
  const fetchStatus = useCallback(() => {
    const mine = ++request.current;
    apiClient
      .getSupervisorStatus()
      .then((s) => {
        if (mine === request.current) setStatus(s);
      })
      // Quiet: a failed read keeps the last status shown, and the next refresh corrects it.
      .catch(() => {});
  }, []);

  useEffect(() => {
    fetchStatus();
    const timer = setInterval(fetchStatus, SUPERVISOR_POLL_MS);
    return () => clearInterval(timer);
  }, [fetchStatus]);

  // Until the first answer there is nothing to link to; a failed read keeps the last status.
  const connected = status !== null && (status.last_polled_at !== null || status.slots.length > 0);

  return (
    <div className="supervisors-page">
      <Link to="/workers" className="supervisors-page__back">
        ← Back to workers
      </Link>
      <h2>Add a machine</h2>

      <section className="supervisors-page__section">
        <h3>1. Download the setup script</h3>
        <p>
          The script is for this tenant and carries no credential, so there is nothing in it to protect. Save it on
          the machine that will run workers.
        </p>
        <a className="supervisors-page__download" href={SETUP_SCRIPT_URL} download={SETUP_SCRIPT_FILENAME}>
          Download setup script
        </a>
      </section>

      <section className="supervisors-page__section">
        <h3>2. Run it, set the supervisor up, then start it</h3>
        <Command value={`bash ${SETUP_SCRIPT_FILENAME}`} label="the setup script command" />
        <Command value="slop-haus setup" label="the setup command" />
        <Command value="slop-haus start" label="the start command" />
        <p>
          The script only runs <code>slop-haus setup</code> itself when it has a terminal; run it yourself if it did
          not (it is safe to repeat). Afterwards, <code>slop-haus status</code> shows what is running,{" "}
          <code>slop-haus logs</code> its log and <code>slop-haus stop</code> stops it.
        </p>
        <p>
          The machine needs uv, the harness this tenant uses (claude, pi or codex), git that can push over SSH or HTTPS,
          docker and enough disk; the setup script checks them. See the <Link to="/help/$slug" params={{ slug: "supervisor" }}>supervisor guide</Link>.
        </p>
      </section>

      {connected && (
        <p role="status">
          A supervisor has connected. Add and manage its workers on the <Link to="/workers">Workers page</Link>.
        </p>
      )}

      <AddSlotManually />
    </div>
  );
}
