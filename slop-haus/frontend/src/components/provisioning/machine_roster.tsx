import type { ReactNode } from "react";
import { apiClient, type Agent, type SupervisorStatus } from "@/api";
import { usePolledResource } from "@/lib/use_polled_resource";
import ProvisioningGate from "./provisioning_gate";
import SupervisorGroup from "./supervisor_group";
import "./provisioning.scss";

interface Props {
  // The workers to list, after the roster's own filters.
  workers: Agent[];
  // Every worker, filtered out or not.
  allWorkers: Agent[];
  status: SupervisorStatus | undefined;
  // The roster's list of rows; controlsFor gives a row what its machine's
  // document adds, and is absent where no machine is involved.
  renderList: (agents: Agent[], controlsFor?: (agent: Agent) => ReactNode) => ReactNode;
}

// The workers grouped by the machine that runs them. With the tenant's
// provisioning off there are no machines to manage, so it is the plain list.
export default function MachineRoster({ workers, allWorkers, status, renderList }: Props) {
  return (
    <ProvisioningGate fallback={renderList(workers)}>
      <Groups workers={workers} allWorkers={allWorkers} status={status} renderList={renderList} />
    </ProvisioningGate>
  );
}

function Groups({ workers, allWorkers, status, renderList }: Props) {
  const supervisors = usePolledResource(() => apiClient.listProvisionSupervisors(), {
    fallbackError: "Could not load the supervisors.",
  });
  // Disabling a supervisor takes its slots with it, and it never polls again, so
  // nothing could be added to it and its removed agents would read "Retiring" forever.
  const live = supervisors.data?.filter((s) => s.archived_at === null && s.disabled_at === null);
  // Whatever the supervisors call does, the roster itself stays: a failed read costs the grouping, not the workers.
  if (live === undefined) {
    return (
      <>
        {supervisors.error !== null && <p role="alert">{supervisors.error}</p>}
        {renderList(workers)}
      </>
    );
  }

  const liveIds = new Set(live.map((s) => s.id));
  const onMachine = (a: Agent) => a.supervisor_agent_id !== undefined && liveIds.has(a.supervisor_agent_id);
  const elsewhere = workers.filter((a) => !onMachine(a));
  return (
    <>
      {live.length === 0 && (
        <p>No machine runs workers yet. Use Add a machine, below, to set one up.</p>
      )}
      {live.map((s) => {
        const stale = status?.supervisors.find((p) => p.agent_id === s.id && p.protocol_status === "stale");
        return (
          <SupervisorGroup
            key={s.id}
            supervisor={s}
            online={status === undefined ? undefined : !status.offline}
            protocolNote={
              stale === undefined || status === undefined
                ? null
                : `${s.name} speaks supervisor protocol ${stale.protocol}; the server is on ${status.current_protocol}. Run slop-haus update && slop-haus stop && slop-haus start on its machine: update only swaps the files, the running supervisor needs a restart.`
            }
            rosterNames={new Set(allWorkers.filter((a) => a.supervisor_agent_id === s.id).map((a) => a.name))}
            onSupervisorChanged={supervisors.reload}
          >
            {(controlsFor) =>
              renderList(
                workers.filter((a) => a.supervisor_agent_id === s.id),
                controlsFor,
              )
            }
          </SupervisorGroup>
        );
      })}
      {elsewhere.length > 0 && (
        live.length === 0 ? (
          renderList(elsewhere)
        ) : (
          <section className="provisioning__group" aria-label="Not on a supervisor">
            <header className="provisioning__group-head">
              <h3>Not on a supervisor</h3>
            </header>
            {renderList(elsewhere)}
          </section>
        )
      )}
    </>
  );
}
