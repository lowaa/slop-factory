import { useCallback, useEffect, useRef, useState } from "react";
import { apiClient, type Conductor, type Conversation } from "@/api";
import { errorMessage } from "@/lib/use_async_action";
import { ownConductors } from "./usable_conductors";

// How often the list is read again on its own: a host going away or coming
// back, and a session replaced from another tab, happen without any request
// of this page's.
const MACHINES_REFRESH_MS = 20_000;

// A machine is one of the person's own remote controls, with the sessions on
// it. A disabled one is kept so it can be enabled again, with no sessions: the
// server refuses it every conversation route. online is null when its host could not be read: neither claimed up nor
// down. sessions is null when they could not be read and none were read
// before: an empty list would say there are none.
export interface Machine {
  conductor: Conductor;
  online: boolean | null;
  // What the host that polled last said it runs, and what the server ships now;
  // absent when the host could not be read.
  build?: HostBuild;
  sessions: Conversation[] | null;
}

export type MachinesState =
  | { status: "loading" }
  | { status: "error"; message: string }
  // removed are the archived ones, which have no sessions on show.
  | { status: "loaded"; machines: Machine[]; removed: Conductor[] };

export interface UseMachines {
  state: MachinesState;
  reload: () => void;
}

export interface HostBuild {
  // "" for a host that reports none: on older code, or not run from the installed package.
  version: string;
  current: string;
}

// updateAvailable is whether the host reports a build other than the one the
// server ships. A host that reports none is not called behind: it may be one
// that cannot say (a checkout), and a hint it could not act on would be noise.
export const updateAvailable = (b: HostBuild | undefined): boolean =>
  b !== undefined && b.version !== "" && b.current !== "" && b.version !== b.current;

// isUsable is whether a machine may start and open sessions.
export const isUsable = (m: Machine) => m.conductor.disabled_at === null;

// useMachines reads the person's remote controls, whether each one's host is
// there and the sessions on each, composed from the per-remote-control
// routes. One machine's reads failing (switched off a moment ago, say) leaves
// the others, and the machine itself, in the list.
export function useMachines(): UseMachines {
  const [state, setState] = useState<MachinesState>({ status: "loading" });
  // Numbered, so a slow older read cannot put back a list that a newer one
  // has replaced.
  const issued = useRef(0);
  // What the last read found, for a read that fails to fall back on.
  const known = useRef(new Map<string, Machine>());

  const load = useCallback(async () => {
    const request = ++issued.current;
    try {
      const { live, removed } = ownConductors(await apiClient.listConductors({ includeArchived: true }));
      const machines = await Promise.all(
        live.map(async (conductor): Promise<Machine> => {
          if (conductor.disabled_at !== null) return { conductor, online: null, sessions: [] };
          const [host, sessions] = await Promise.allSettled([
            apiClient.getConductorHost(conductor.id),
            apiClient.listConversations(conductor.id),
          ]);
          return {
            conductor,
            online: host.status === "fulfilled" ? host.value.online : null,
            build:
              host.status === "fulfilled"
                ? { version: host.value.version ?? "", current: host.value.current_version ?? "" }
                : undefined,
            sessions: sessions.status === "fulfilled" ? sessions.value : (known.current.get(conductor.id)?.sessions ?? null),
          };
        }),
      );
      if (request !== issued.current) return;
      known.current = new Map(machines.map((m) => [m.conductor.id, m]));
      setState({ status: "loaded", machines, removed });
    } catch (err) {
      if (request !== issued.current) return;
      // A refresh that fails leaves the list on show.
      setState((was) =>
        was.status === "loaded" ? was : { status: "error", message: errorMessage(err, "Failed to load your remote controls.") },
      );
    }
  }, []);

  useEffect(() => {
    void load();
    const timer = setInterval(() => void load(), MACHINES_REFRESH_MS);
    return () => clearInterval(timer);
  }, [load]);

  return { state, reload: () => void load() };
}
