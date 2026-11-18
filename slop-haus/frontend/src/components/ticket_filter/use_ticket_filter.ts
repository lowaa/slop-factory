import { useCallback, useEffect, useState } from "react";
import { useUsage } from "@/usage/use_usage";
import { rememberFilterSearch } from "./filter_memory";
import type { SortId, SortModel } from "@/lib/ticket_query/ticket_sort";
import type { FieldValue, FilterSearch, FilterValues, TicketFilterModel } from "@/domain/ticket_filter_model";

// Long enough that typing a word is one change rather than one per letter,
// short enough that the page still feels like it follows the box.
export const TEXT_DEBOUNCE_MS = 250;

export interface TicketFilterOptions {
  model: TicketFilterModel;
  values: FilterValues;
  onChange: (values: FilterValues) => void;
  // The page's sort, beside the filter and not part of it: changing it does
  // not count as filtering, and "clear" leaves it alone.
  sort?: { model: SortModel; value: SortId; onChange: (sort: SortId) => void };
  // Address parts that are the page's, not the filter's (a page cursor),
  // remembered with the filter for the Back link.
  extraSearch?: FilterSearch;
}

// useTicketFilter owns what the model leaves to a running page: the text
// being typed ahead of the filter, when it is handed on, applying and
// clearing, and the Back-link memory. The values themselves stay the
// caller's, so the address can hold them.
export function useTicketFilter({ model, values, onChange, sort, extraSearch }: TicketFilterOptions) {
  const textId = model.textField?.id;
  const text = textId ? model.text(values, textId) : "";
  const usage = useUsage();

  // The box shows what is being typed, which runs ahead of the filter's
  // text until the typing pauses and it is handed on.
  const [draft, setDraft] = useState(text);
  // A text other than the one last seen came from outside -- this box's own
  // text arriving back, or something else such as the header's Board tab
  // clearing the address. Only the second replaces the draft: the first can
  // land after more has been typed, and must not undo it.
  const [seen, setSeen] = useState(text);
  const [sent, setSent] = useState(text);
  if (text !== seen) {
    setSeen(text);
    if (text !== sent) {
      setDraft(text);
      setSent(text);
    }
  }

  useEffect(() => {
    if (!textId || draft === text || draft === sent) return;
    const timer = setTimeout(() => {
      setSent(draft);
      usage.event("filter_used", { filter: "ticket_text", has_text: draft.trim() !== "" });
      onChange({ ...values, [textId]: draft });
    }, TEXT_DEBOUNCE_MS);
    return () => clearTimeout(timer);
  }, [textId, draft, sent, text, values, onChange, usage]);

  // With the draft, not the filter's text: text still waiting on its pause,
  // or handed on but not yet back, would otherwise be undone by this change
  // landing after it.
  const setField = useCallback(
    (id: string, value: FieldValue) => {
      usage.event("filter_used", { filter: "ticket_field" });
      const next = { ...values, [id]: value };
      if (textId && id !== textId) {
        setSent(draft);
        next[textId] = draft;
      }
      onChange(next);
    },
    [values, textId, draft, onChange, usage],
  );

  const clear = useCallback(() => {
    usage.event("filter_used", { filter: "ticket_clear" });
    setDraft("");
    setSent("");
    onChange(model.defaults());
  }, [model, onChange, usage]);

  // Remembered cleared as well as set: the Back link returns to the page as
  // it was last seen. Compared as text, so a caller rebuilding an equal
  // search each render doesn't rerun it.
  const search = JSON.stringify({ ...model.toSearch(values), ...(sort && sort.model.toSearch(sort.value)), ...extraSearch });
  const key = model.key;
  useEffect(() => {
    rememberFilterSearch(key, JSON.parse(search) as FilterSearch);
  }, [key, search]);

  return { draft, setDraft, setField, clear, filtered: model.isFiltered(values), activeCount: model.activeCount(values) };
}
