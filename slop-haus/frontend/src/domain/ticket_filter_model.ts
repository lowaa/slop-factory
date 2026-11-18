import type { Priority, Status } from "@/api";
import { parseStatusList } from "./status";
import { BOARD_STATUSES } from "./board_columns";

// The model of a ticket filter: which fields a page's filter has, their
// defaults, and how they convert to and from the address. Plain data and
// functions, so it is tested without React; the controller
// (use_ticket_filter.ts) and the view (ticket_filter.tsx) only follow it.
//
// Adding a field type is one entry in each of FieldSpec, FieldValue and
// KINDS below, plus its control in ticket_filter.tsx.

export const FILTER_PRIORITIES: readonly Priority[] = ["low", "normal", "high", "urgent"];

// The board has no column for cancelled, but a filter may still name it.
export const FILTER_STATUSES: readonly Status[] = [...BOARD_STATUSES, "cancelled"];

export interface DateRange {
  // UTC days, YYYY-MM-DD, inclusive; empty is unbounded.
  from: string;
  to: string;
}

// What an address holds: strings, and flags that are present or absent.
export type FilterSearch = Record<string, string | true | undefined>;

export interface TextFieldSpec {
  type: "text";
  id: string;
  param: string;
  label: string;
  placeholder?: string;
  // Offer a jump to the ticket when the text names one ("#85").
  ticketJump?: boolean;
}

// A flag is off by default; its address param is ?param=true or absent.
export interface FlagFieldSpec {
  type: "flag";
  id: string;
  param: string;
  label: string;
  // The status this flag adds to what the server is asked for when it is
  // on, for a flag that stands for a status (the board's Done and Parked).
  status?: Status;
}

// Which statuses are left out. The default set is per instance, and the
// address only holds the set when it differs from that default.
export interface StatusFieldSpec {
  type: "status";
  id: string;
  param: string;
  defaultExcluded: readonly Status[];
}

export interface PriorityFieldSpec {
  type: "priority";
  id: string;
  param: string;
}

// label says which date it filters on, e.g. "Merged" for Merged from / to.
export interface DateRangeFieldSpec {
  type: "date_range";
  id: string;
  fromParam: string;
  toParam: string;
  label: string;
  // The ticket date the server filters on, which names its params
  // (`closed` is closed_from / closed_to).
  dateField: string;
}

export type FieldSpec = TextFieldSpec | FlagFieldSpec | StatusFieldSpec | PriorityFieldSpec | DateRangeFieldSpec;

// Empty is every priority.
export type FieldValue = string | boolean | readonly Status[] | DateRange;
export type FilterValues = Record<string, FieldValue>;

// What the server is asked for, per its param: repeated ones (status,
// priority) are lists, the rest strings. Unset ones are absent.
export type QueryParams = Record<string, string | readonly string[] | undefined>;

export interface FilterQuery {
  // Always explicit, so the server's own default never decides. Empty means
  // every status is unticked: nothing can match, and a request with none
  // would ask for them all.
  status: Status[];
  params: QueryParams;
}

interface FieldKind<S extends FieldSpec> {
  initial(spec: S): FieldValue;
  read(spec: S, raw: Record<string, unknown>): FieldValue;
  // Defaults are left out, so an unfiltered page keeps a plain address.
  write(spec: S, value: FieldValue): FilterSearch;
  params(spec: S): string[];
  // The API params the value stands for. A status contribution is merged
  // across fields by the model.
  query(spec: S, value: FieldValue): QueryParams;
}

// The status set in the address. Empty is spelled out, as an empty param
// is dropped on the way in.
const NO_STATUSES = "none";
const DAY = /^\d{4}-\d{2}-\d{2}$/;

function str(raw: Record<string, unknown>, key: string): string {
  const v = raw[key];
  return typeof v === "string" ? v : "";
}

function inOrder(statuses: Iterable<Status>): Status[] {
  const set = new Set(statuses);
  return FILTER_STATUSES.filter((s) => set.has(s));
}

const KINDS: { [T in FieldSpec["type"]]: FieldKind<Extract<FieldSpec, { type: T }>> } = {
  text: {
    initial: () => "",
    read: (spec, raw) => str(raw, spec.param),
    write: (spec, value) => ({ [spec.param]: (value as string) || undefined }),
    params: (spec) => [spec.param],
    query: (_spec, value) => ({ q: (value as string).trim() ? (value as string) : undefined }),
  },
  flag: {
    initial: () => false,
    read: (spec, raw) => raw[spec.param] === true || raw[spec.param] === "true",
    write: (spec, value) => ({ [spec.param]: value === true ? true : undefined }),
    params: (spec) => [spec.param],
    query: (spec, value) => ({ status: value === true && spec.status ? [spec.status] : undefined }),
  },
  status: {
    initial: (spec) => inOrder(spec.defaultExcluded),
    read: (spec, raw) => {
      const text = str(raw, spec.param);
      if (!text) return inOrder(spec.defaultExcluded);
      if (text === NO_STATUSES) return [];
      const known = parseStatusList(text.split(","));
      // Nothing recognised (a status since renamed) is not "hide nothing".
      return known.length > 0 ? inOrder(known) : inOrder(spec.defaultExcluded);
    },
    write: (spec, value) => {
      const excluded = inOrder(value as readonly Status[]);
      const isDefault = excluded.join() === inOrder(spec.defaultExcluded).join();
      return { [spec.param]: isDefault ? undefined : excluded.join(",") || NO_STATUSES };
    },
    params: (spec) => [spec.param],
    query: (_spec, value) => {
      const excluded = value as readonly Status[];
      return { status: FILTER_STATUSES.filter((s) => !excluded.includes(s)) };
    },
  },
  priority: {
    initial: () => "",
    read: (spec, raw) => {
      const v = str(raw, spec.param);
      return FILTER_PRIORITIES.includes(v as Priority) ? v : "";
    },
    write: (spec, value) => ({ [spec.param]: (value as string) || undefined }),
    params: (spec) => [spec.param],
    query: (_spec, value) => ({ priority: value ? [value as string] : undefined }),
  },
  date_range: {
    initial: () => ({ from: "", to: "" }),
    read: (spec, raw) => {
      const day = (key: string) => (DAY.test(str(raw, key)) ? str(raw, key) : "");
      return { from: day(spec.fromParam), to: day(spec.toParam) };
    },
    write: (spec, value) => {
      const range = value as DateRange;
      return { [spec.fromParam]: range.from || undefined, [spec.toParam]: range.to || undefined };
    },
    params: (spec) => [spec.fromParam, spec.toParam],
    query: (spec, value) => {
      const range = value as DateRange;
      return { [`${spec.dateField}_from`]: range.from || undefined, [`${spec.dateField}_to`]: range.to || undefined };
    },
  },
};

// The kinds are indexed by each spec's own type, which TypeScript cannot
// follow through a lookup; this is the one place that is asserted.
function kindOf(spec: FieldSpec): FieldKind<FieldSpec> {
  return KINDS[spec.type] as unknown as FieldKind<FieldSpec>;
}

export class TicketFilterModel {
  // key names the instance: what the Back link remembers it under, and so
  // what keeps one page's filter from touching another's.
  readonly key: string;
  readonly fields: readonly FieldSpec[];
  // Statuses the page always asks for, whatever the filter: the board's
  // live columns, which only the Done and Parked flags add to.
  readonly baseStatuses: readonly Status[];

  constructor(key: string, fields: readonly FieldSpec[], baseStatuses: readonly Status[] = []) {
    this.key = key;
    this.fields = fields;
    this.baseStatuses = baseStatuses;
    const ids = new Set<string>();
    const params = new Set<string>();
    for (const spec of fields) {
      if (ids.has(spec.id)) throw new Error(`ticket filter ${key}: field ${spec.id} is listed twice`);
      ids.add(spec.id);
      for (const param of kindOf(spec).params(spec)) {
        if (params.has(param)) throw new Error(`ticket filter ${key}: address param ${param} is used twice`);
        params.add(param);
      }
    }
    if (fields.filter((f) => f.type === "text").length > 1) {
      throw new Error(`ticket filter ${key}: at most one text field`);
    }
  }

  // The one text field, whose typing is held back until it pauses.
  get textField(): TextFieldSpec | undefined {
    return this.fields.find((f): f is TextFieldSpec => f.type === "text");
  }

  defaults(): FilterValues {
    return Object.fromEntries(this.fields.map((spec) => [spec.id, kindOf(spec).initial(spec)]));
  }

  // Tolerant of anything an address can hold: what is not understood is
  // the default.
  fromSearch(raw: Record<string, unknown>): FilterValues {
    return Object.fromEntries(this.fields.map((spec) => [spec.id, kindOf(spec).read(spec, raw)]));
  }

  toSearch(values: FilterValues): FilterSearch {
    return Object.assign({}, ...this.fields.map((spec) => kindOf(spec).write(spec, values[spec.id])));
  }

  // The address as a route's validateSearch hands it on. The page types
  // the result as its own address shape (see board_filter.ts).
  parseSearch<S extends object>(raw: Record<string, unknown>): S {
    return this.toSearch(this.fromSearch(raw)) as S;
  }

  // toSearch with every param named, so navigating with it also clears the
  // ones the new values leave out.
  searchPatch(values: FilterValues): FilterSearch {
    const cleared = Object.fromEntries(this.fields.flatMap((spec) => kindOf(spec).params(spec)).map((p) => [p, undefined]));
    return { ...cleared, ...this.toSearch(values) };
  }

  // The filter as the API's query. The statuses always asked for and those
  // a flag adds are what the page can show; a status field narrows them,
  // and stands alone when there are none. A model that names no status at
  // all asks for every one.
  query(values: FilterValues): FilterQuery {
    const params: Record<string, string | string[] | undefined> = {};
    const offered = new Set<Status>(this.baseStatuses);
    let hasOffered = this.baseStatuses.length > 0;
    let fieldIncluded: Status[] | undefined;
    for (const spec of this.fields) {
      const { status: named, ...rest } = kindOf(spec).query(spec, values[spec.id]);
      if (spec.type === "status") fieldIncluded = parseStatusList(named);
      else for (const s of parseStatusList(named)) offered.add(s);
      hasOffered ||= spec.type === "flag" && spec.status !== undefined;
      for (const [k, v] of Object.entries(rest)) if (v !== undefined) params[k] = v as string | string[];
    }
    let status: Status[] = [...FILTER_STATUSES];
    if (hasOffered) status = inOrder(offered);
    if (fieldIncluded) status = status.filter((s) => fieldIncluded.includes(s));
    return { status, params };
  }

  isFiltered(values: FilterValues): boolean {
    return this.activeCount(values) > 0;
  }

  // How many fields differ from the page's defaults. A field counts once
  // however many params it spans (a date range is one), and by what its
  // write leaves in the address, so this is zero exactly when isFiltered is
  // false. Sort is the page's, not a field, and is not counted.
  activeCount(values: FilterValues): number {
    return this.fields.filter((spec) =>
      Object.values(kindOf(spec).write(spec, values[spec.id])).some((v) => v !== undefined),
    ).length;
  }

  text(values: FilterValues, id: string): string {
    return values[id] as string;
  }
  flag(values: FilterValues, id: string): boolean {
    return values[id] as boolean;
  }
  excluded(values: FilterValues, id: string): readonly Status[] {
    return values[id] as readonly Status[];
  }
  priority(values: FilterValues, id: string): Priority | undefined {
    return (values[id] as Priority | "") || undefined;
  }
  range(values: FilterValues, id: string): DateRange {
    return values[id] as DateRange;
  }

  // The statuses to ask the server for, or none to ask for all of them: a
  // page's own default set is what it fetches without being told.
  includedStatuses(values: FilterValues, id: string): Status[] | undefined {
    const spec = this.fields.find((f) => f.id === id);
    if (spec?.type !== "status" || kindOf(spec).write(spec, values[id])[spec.param] === undefined) return undefined;
    const excluded = this.excluded(values, id);
    return FILTER_STATUSES.filter((s) => !excluded.includes(s));
  }
}
