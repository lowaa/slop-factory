import { useEffect, useState } from "react";
import { apiClient, type VariantOption } from "@/api";
import { errorMessage } from "@/lib/use_async_action";
import { timestampsFrom, type TicketStore } from "./use_ticket_store";

export interface VariantOptions {
  options: VariantOption[];
  loadFailed: boolean;
}

// The unarchived variant versions, fetched once like the tenant's roles: the
// same list for every ticket. loadFailed is kept apart from an empty list so
// a failed fetch does not read as "nothing to choose".
export function useVariantOptions(): VariantOptions {
  const [options, setOptions] = useState<VariantOption[]>([]);
  const [loadFailed, setLoadFailed] = useState(false);
  useEffect(() => {
    let cancelled = false;
    apiClient
      .listVariantOptions()
      .then((loaded) => {
        if (!cancelled) setOptions(loaded);
      })
      .catch(() => {
        if (!cancelled) setLoadFailed(true);
      });
    return () => {
      cancelled = true;
    };
  }, []);
  return { options, loadFailed };
}

export interface VariantPickerActions {
  saving: boolean;
  error: string | null;
  choose: (versionId: string | null) => void;
}

// useVariantPicker routes an attempt of the ticket to a variant version. The
// picker is disabled while a choice is in flight, so there is never a second
// one to order against. A refused change (the attempt was leased meanwhile) is
// reported beside the picker, and the reply's attempts replace the view so the
// picker goes away if it can no longer be used.
//
// attempt is the one the page is of, when the ticket has several; otherwise
// the ticket's only attempt is routed through the ticket's own endpoint.
export function useVariantPicker(store: TicketStore, ticketId: number, attempt?: number): VariantPickerActions {
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function save(versionId: string | null) {
    if (store.state.status !== "loaded") return;
    setSaving(true);
    setError(null);
    // Replies carry every attempt as of their own write, so a delayed one
    // must not put back an older list over a newer write's.
    const generation = store.beginFieldWrite("attempts");
    try {
      const updated =
        attempt === undefined
          ? await apiClient.setTicketVariant(ticketId, versionId)
          : await apiClient.setAttemptVariant(ticketId, attempt, versionId);
      if (!store.isCurrent("attempts", generation)) return;
      store.updateLoaded((prev) => ({
        ...prev,
        ticket: {
          ...prev.ticket,
          attempts: updated.attempts,
          no_eligible_agent: updated.no_eligible_agent,
          ...timestampsFrom(prev.ticket, updated, false),
        },
      }));
    } catch (err) {
      setError(errorMessage(err, "Failed to route the ticket to that variant."));
    } finally {
      setSaving(false);
    }
  }

  return { saving, error, choose: (versionId) => void save(versionId) };
}
