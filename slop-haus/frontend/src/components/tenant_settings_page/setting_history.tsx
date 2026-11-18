import { useCallback, useEffect, useRef, useState } from "react";
import { apiClient, type ServerSettingChange, type TenantSettingChange } from "@/api";
import { useAuth } from "@/context/auth";
import { changedByLabel, formatSettingValue } from "./setting_format";
import Timestamp from "@/shared/timestamp/timestamp";
import { errorMessage } from "@/lib/use_async_action";

export function ChangedBy({ change }: { change: ServerSettingChange }) {
  const { profile } = useAuth();
  return <>{changedByLabel(change, profile?.id)}</>;
}

const PAGE_SIZE = 20;

// One row of a history, whichever endpoint it came from.
export interface HistoryEntry {
  id: number;
  at: string;
  // Absent when the history is always the viewer's own.
  by?: React.ReactNode;
  // Absent when the endpoint does not say what the setting read before.
  from?: string;
  to: string;
  note?: string;
  // Made before the viewer's current membership began, so what came before
  // it says nothing about what the setting read at the start of this one.
  earlierMembership?: boolean;
}

export interface HistoryPage {
  entries: HistoryEntry[];
  nextBefore?: number;
}

export type LoadHistoryPage = (page: { limit: number; before?: number }) => Promise<HistoryPage>;

export function loadServerHistory(name: string): LoadHistoryPage {
  return async (page) => {
    const res = await apiClient.getSettingHistory(name, page);
    return {
      entries: res.changes.map((c) => ({
        id: c.id,
        at: c.changed_at,
        by: <ChangedBy change={c} />,
        from: formatSettingValue(c.from),
        to: formatSettingValue(c.to),
      })),
      nextBefore: res.next_before,
    };
  };
}

// The per-user endpoint reports only the value each change set, so what a
// setting read before a change is the value of the next older entry. The
// oldest entry, which was set from the default, has no "from" to show.
// A per-user change carries only the value it set, in the same fields as a
// UserSetting.
function formatTenantSettingChange(change: TenantSettingChange): string {
  switch (change.type) {
    case "bool":
      return change.bool_val ? "on" : "off";
    case "int":
      return String(change.int_val);
    case "float":
      return String(change.float_val);
    case "str":
      return `"${change.str_val ?? ""}"`;
  }
}

export function loadMyTenantHistory(name: string): LoadHistoryPage {
  return async (page) => {
    const res = await apiClient.getMyTenantSettingHistory(name, page);
    return {
      entries: res.changes.map((c) => ({
        id: c.id,
        at: c.set_at,
        to: formatTenantSettingChange(c),
        earlierMembership: c.earlier_membership,
        note: c.earlier_membership ? "before you last joined this tenant" : undefined,
      })),
      nextBefore: res.next_before,
    };
  };
}

function withPreviousAsFrom(entries: HistoryEntry[]): HistoryEntry[] {
  return entries.map((e, i) => {
    const older = entries[i + 1];
    const acrossReset = !!older && !!older.earlierMembership && !e.earlierMembership;
    return { ...e, from: e.from ?? (acrossReset ? undefined : older?.to) };
  });
}

type HistoryState =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "loaded"; entries: HistoryEntry[]; nextBefore?: number; loadingMore: boolean };

interface SettingHistoryProps {
  // Identifies the setting: the list reloads when it changes.
  name: string;
  // Defaults to the tenant-wide setting's history.
  load?: LoadHistoryPage;
  // The endpoint gives each entry's new value only; show the next older
  // entry's value as the old one.
  previousAsFrom?: boolean;
  emptyText?: string;
  // Changes whenever the setting is written, so the list shown follows it.
  version: string | null;
}

export default function SettingHistory({
  name,
  load = loadServerHistory(name),
  previousAsFrom,
  emptyText = "Never changed from its default.",
  version,
}: SettingHistoryProps) {
  const [state, setState] = useState<HistoryState>({ status: "loading" });
  const [moreError, setMoreError] = useState<string | null>(null);

  // Bumped whenever the list is reloaded, so an older page still in flight
  // cannot be appended to a list it no longer belongs to.
  const generation = useRef(0);
  // Callers build load afresh each render; name and version decide when to
  // reload, so the latest one is read through a ref.
  const loadRef = useRef(load);
  loadRef.current = load;

  useEffect(() => {
    let current = true;
    generation.current++;
    loadRef.current({ limit: PAGE_SIZE })
      .then((page) => {
        if (!current) return;
        setState({ status: "loaded", entries: page.entries, nextBefore: page.nextBefore, loadingMore: false });
      })
      .catch((err: unknown) => {
        if (!current) return;
        setState({ status: "error", message: errorMessage(err, "Failed to load the history.") });
      });
    return () => {
      current = false;
    };
    }, [name, version]);

  const loadMore = useCallback(async () => {
    if (state.status !== "loaded" || state.nextBefore === undefined || state.loadingMore) return;
    setState({ ...state, loadingMore: true });
    setMoreError(null);
    const requested = generation.current;
    try {
      const page = await loadRef.current({ limit: PAGE_SIZE, before: state.nextBefore });
      if (requested !== generation.current) return;
      setState({
        status: "loaded",
        entries: [...state.entries, ...page.entries],
        nextBefore: page.nextBefore,
        loadingMore: false,
      });
    } catch (err: unknown) {
      if (requested !== generation.current) return;
      setMoreError(errorMessage(err, "Failed to load older changes."));
      setState({ ...state, loadingMore: false });
    }
  }, [state]);

  if (state.status === "loading") {
    return <p className="tenant-settings__muted">Loading history…</p>;
  }
  if (state.status === "error") {
    return (
      <p role="alert" className="tenant-settings__error">
        {state.message}
      </p>
    );
  }
  if (state.entries.length === 0) {
    return <p className="tenant-settings__muted">{emptyText}</p>;
  }
  return (
    <div>
      <ul className="tenant-settings__history" aria-label="Changes">
        {(previousAsFrom ? withPreviousAsFrom(state.entries) : state.entries).map((entry) => (
          <li key={entry.id} className="tenant-settings__change">
            {entry.by !== undefined && <span>{entry.by}</span>}
            <Timestamp at={entry.at} />
            <span className="tenant-settings__change-values">
              {entry.from !== undefined ? `${entry.from} → ${entry.to}` : `set to ${entry.to}`}
            </span>
            {entry.note && <span className="tenant-settings__muted">{entry.note}</span>}
          </li>
        ))}
      </ul>
      {state.nextBefore !== undefined && (
        <button type="button" className="tenant-settings__link-button" disabled={state.loadingMore} onClick={loadMore}>
          Show older changes
        </button>
      )}
      {moreError && (
        <p role="alert" className="tenant-settings__error">
          {moreError}
        </p>
      )}
    </div>
  );
}
