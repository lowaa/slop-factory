import { errorMessage } from "../use_async_action";
import type { Ticket } from "@/api";
import type { Logger } from "@slop-factory/logging/logger";
import type { QueryParams } from "@/domain/ticket_filter_model";
import type { TicketApiQuery, TicketQuery, TicketQueryModel } from "../ticket_query/ticket_query";
import { matchTicket, type TicketMatcher } from "./ticket_matcher";
import { cardKey, cardsOf } from "./card_key";
import { compareCards, isOlder } from "./ticket_order";

export interface FetchedPage {
  tickets: Ticket[];
  nextCursor?: string;
}

export type FetchPage = (params: QueryParams, signal: AbortSignal) => Promise<FetchedPage>;

// Where the live changes come from. The controller subscribes itself, so a
// component never sees an event.
export interface LiveChanges {
  // cards is the changed ticket's attempt cards in the work columns, in
  // full, when it has several attempts (see cardsOf).
  onTicket(ticket: Ticket, cards?: readonly Ticket[]): void;
  onReload(): void;
}
export type SubscribeChanges = (changes: LiveChanges) => () => void;

export interface PagedCollectionDeps {
  // Names the list ("unreviewed", "board:done"); two lists never share state
  // because each has its own instance, and the key labels this one's logs.
  key: string;
  queryModel: TicketQueryModel;
  fetchPage: FetchPage;
  logger: Logger;
  subscribe?: SubscribeChanges;
  matcher?: TicketMatcher;
}

export interface PagedSnapshot {
  items: readonly Ticket[];
  // A first page (or reload) is in flight. Items already shown stay while a
  // reload runs.
  loading: boolean;
  loadingMore: boolean;
  hasMore: boolean;
  // The first page has arrived, or the query matches nothing.
  loaded: boolean;
  error: string | null;
}

const INITIAL: PagedSnapshot = { items: [], loading: false, loadingMore: false, hasMore: false, loaded: false, error: null };

function isAbort(err: unknown): boolean {
  return err instanceof DOMException && err.name === "AbortError";
}

// One paged list: its pages, cursor and request state, kept consistent with
// live ticket changes. Requests are cancelled and their replies ignored once
// a newer one has been made, so a slow reply can never overwrite a fresher
// list.
export class PagedCollection {
  private readonly deps: PagedCollectionDeps;
  private readonly matcher: TicketMatcher;
  private readonly listeners = new Set<() => void>();
  private snapshot: PagedSnapshot = INITIAL;
  private query: TicketQuery | null = null;
  private queryKey: string | null = null;
  private cursor: string | undefined;
  private inflight: { id: number; controller: AbortController; first: boolean } | null = null;
  private requests = 0;
  // A change arrived while the first page was on its way, so that page may
  // not show it: fetch again once it lands.
  private staleFirstPage = false;
  // Newest change seen live of each ticket while a request is out, with the
  // cards it left the ticket as. A page that was read before the change can
  // carry an older copy; without this a card the change removed would come
  // back with that page, or an edit would be undone.
  private liveTickets = new Map<number, { ticket: Ticket; cards: Ticket[] }>();
  // The newest change applied of each ticket, so an older one that arrives
  // late (several cards are added and dropped by one change, so there is no
  // shown card to compare it with) cannot put back a card a newer one dropped.
  private appliedTickets = new Map<number, Ticket>();
  // Which request the shown error belongs to, so retry repeats that one.
  private failed: "first" | "more" | null = null;
  private unsubscribe: (() => void) | null = null;
  private closed = false;

  constructor(deps: PagedCollectionDeps) {
    this.deps = deps;
    this.matcher = deps.matcher ?? matchTicket;
  }

  start(query: TicketQuery): void {
    if (this.closed) return;
    this.unsubscribe ??= this.deps.subscribe?.({
      onTicket: (t, cards) => this.applyTicket(t, cards),
      onReload: () => this.reload(),
    }) ?? null;
    this.setQuery(query);
  }

  subscribe = (listener: () => void): (() => void) => {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  };

  getSnapshot = (): PagedSnapshot => this.snapshot;

  // A different list (by the query's key) starts over; the same one, however
  // far it has paged, is left alone.
  setQuery(query: TicketQuery): void {
    if (this.closed) return;
    const key = this.deps.queryModel.key(query);
    if (key === this.queryKey) return;
    this.queryKey = key;
    this.query = { ...query, page: { limit: query.page.limit } };
    this.reset();
    void this.fetchFirst();
  }

  loadMore(): void {
    if (this.closed || !this.query || this.inflight || !this.snapshot.hasMore || !this.snapshot.loaded) return;
    void this.fetchMore();
  }

  // Fetch the first page again, for a failure or a change the list cannot
  // work out for itself. It replaces everything loaded so far.
  reload(): void {
    if (this.closed || !this.query) return;
    this.abortInflight();
    void this.fetchFirst();
  }

  // Try again after an error: the page that failed.
  retry(): void {
    if (this.failed === "more") this.loadMore();
    else if (this.failed === "first") this.reload();
  }

  // The rules for a changed ticket, applied to what is loaded. A ticket is
  // one card, or several when cards are given: the cards it now has replace
  // every card of it the list shows, and a card that is gone or moved to
  // another column is dropped from this one.
  applyTicket(ticket: Ticket, cards: readonly Ticket[] = []): void {
    if (this.closed || !this.query) return;
    const api = this.deps.queryModel.toApi(this.query);
    if (api.matchesNothing) return;
    const applied = this.appliedTickets.get(ticket.id);
    if (applied && isOlder(ticket, applied)) return;
    this.appliedTickets.set(ticket.id, ticket);
    const now = cardsOf(ticket, cards);
    if (this.inflight) {
      const seen = this.liveTickets.get(ticket.id);
      if (!seen || !isOlder(ticket, seen.ticket)) this.liveTickets.set(ticket.id, { ticket, cards: now });
    }
    // A first page on its way may not include this change.
    if (this.inflight?.first) this.staleFirstPage = true;
    if (!this.snapshot.loaded) return;
    const verdicts = now.map((card) => ({ card, verdict: this.matcher(card, api) }));
    const fits = verdicts.filter((v) => v.verdict !== "no_match").map((v) => v.card);
    const keep = new Set(fits.map(cardKey));
    // Cards of the ticket shown here that it no longer has in this list.
    // Not ones a newer copy than this change has already replaced.
    let items = this.snapshot.items.filter((t) => t.id !== ticket.id || keep.has(cardKey(t)) || isOlder(ticket, t));
    let changed = items.length !== this.snapshot.items.length;
    for (const { card, verdict } of verdicts) {
      if (verdict === "no_match") continue;
      const at = items.findIndex((t) => cardKey(t) === cardKey(card));
      if (at >= 0) {
        // An event older than what is shown (a reply that overtook it).
        if (isOlder(card, items[at])) continue;
        const rest = items.filter((_, i) => i !== at);
        // Its sort position changed (closing moves a ticket in recently
        // closed): place it again as if it were new.
        items = this.staysAt(items, at, card) ? items.map((t, i) => (i === at ? card : t)) : this.place(rest, card);
        changed = true;
      } else if (verdict === "needs_server") {
        this.deps.logger.debug("paged collection: reloading for a change only the server can match", { key: this.deps.key, ticket: ticket.id });
        this.reload();
        return;
      } else {
        // Matches and is not loaded.
        items = this.place(items, card);
        changed = true;
      }
    }
    if (changed) this.publish({ items });
  }

  close(): void {
    if (this.closed) return;
    this.closed = true;
    this.abortInflight();
    this.unsubscribe?.();
    this.unsubscribe = null;
    this.listeners.clear();
  }

  private compare(a: Ticket, b: Ticket): number {
    return compareCards(this.query!.sort)(a, b);
  }

  // Whether the ticket still sorts between its neighbours.
  private staysAt(items: readonly Ticket[], at: number, ticket: Ticket): boolean {
    const prev = items[at - 1];
    const next = items[at + 1];
    return (!prev || this.compare(prev, ticket) < 0) && (!next || this.compare(ticket, next) < 0);
  }

  // Puts a ticket at its sort position, but only when that is within what is
  // loaded; beyond the last loaded one a later page brings it.
  private place(items: readonly Ticket[], ticket: Ticket): Ticket[] {
    const last = items[items.length - 1];
    if (this.snapshot.hasMore && last && this.compare(ticket, last) > 0) return [...items];
    const at = items.findIndex((t) => this.compare(ticket, t) < 0);
    const out = [...items];
    out.splice(at < 0 ? out.length : at, 0, ticket);
    return out;
  }

  private reset(): void {
    this.abortInflight();
    this.cursor = undefined;
    this.staleFirstPage = false;
    this.failed = null;
    this.liveTickets.clear();
    this.appliedTickets.clear();
    this.snapshot = INITIAL;
    this.notify();
  }

  private abortInflight(): void {
    this.inflight?.controller.abort();
    this.inflight = null;
  }

  private async fetchFirst(): Promise<void> {
    const query = this.query;
    if (!query) return;
    const api = this.deps.queryModel.toApi(query);
    if (api.matchesNothing) {
      this.publish({ items: [], loaded: true, hasMore: false, loading: false, loadingMore: false, error: null });
      return;
    }
    this.staleFirstPage = false;
    this.failed = null;
    // Also ends a "load more" that reload() cancelled, in the same update, so
    // the spinner does not blink off and on.
    this.publish({ loading: true, loadingMore: false, error: null });
    const page = await this.request(true, api.params);
    if (!page) return;
    this.cursor = page.nextCursor;
    this.publish({ items: this.merge([], page.tickets, api), hasMore: page.nextCursor !== undefined, loaded: true, loading: false });
    this.liveTickets.clear();
    if (this.staleFirstPage) this.reload();
  }

  private async fetchMore(): Promise<void> {
    if (!this.query || this.cursor === undefined) return;
    const api = this.deps.queryModel.toApi({ ...this.query, page: { limit: this.query.page.limit, cursor: this.cursor } });
    this.failed = null;
    this.publish({ loadingMore: true, error: null });
    const page = await this.request(false, api.params);
    if (!page) return;
    this.cursor = page.nextCursor;
    this.publish({ items: this.merge(this.snapshot.items, page.tickets, api), hasMore: page.nextCursor !== undefined, loadingMore: false });
    this.liveTickets.clear();
  }

  // null when the request failed, was cancelled or was overtaken: the caller
  // has nothing to apply.
  private async request(first: boolean, params: QueryParams): Promise<FetchedPage | null> {
    const id = ++this.requests;
    const controller = new AbortController();
    this.inflight = { id, controller, first };
    try {
      const page = await this.deps.fetchPage(params, controller.signal);
      if (controller.signal.aborted || this.inflight?.id !== id) return null;
      this.inflight = null;
      return page;
    } catch (err) {
      if (controller.signal.aborted || this.inflight?.id !== id || isAbort(err)) return null;
      this.inflight = null;
      this.deps.logger.error("paged collection: fetching a page failed", { key: this.deps.key, error: String(err) });
      this.failed = first ? "first" : "more";
      this.publish({ loading: false, loadingMore: false, error: errorMessage(err, "Could not load tickets.") });
      return null;
    }
  }

  // A page can repeat a card already shown (something moved while paging)
  // or carry an older copy of one a live change has updated: the newer copy
  // wins, and a card the live change no longer gives this list is left out.
  // The server's order is kept as it came; only a live insertion is placed
  // by the sort.
  private merge(shown: readonly Ticket[], incoming: readonly Ticket[], api: TicketApiQuery): Ticket[] {
    const out = [...shown];
    const at = new Map(out.map((t, i) => [cardKey(t), i]));
    for (const page of incoming) {
      let t = page;
      const live = this.liveTickets.get(t.id);
      if (live) {
        // A change to one attempt need not move the ticket's own stamp, so a
        // card that is still there is compared by its own.
        const current = live.cards.find((c) => cardKey(c) === cardKey(t));
        if (!current ? isOlder(t, live.ticket) : isOlder(t, current)) {
          if (!current || this.matcher(current, api) === "no_match") continue;
          t = current;
        }
      }
      const key = cardKey(t);
      const i = at.get(key);
      if (i === undefined) {
        at.set(key, out.length);
        out.push(t);
      } else if (!isOlder(t, out[i])) {
        out[i] = t;
      }
    }
    return out;
  }

  private publish(patch: Partial<PagedSnapshot>): void {
    if (this.closed) return;
    this.snapshot = { ...this.snapshot, ...patch };
    this.notify();
  }

  private notify(): void {
    for (const l of [...this.listeners]) l();
  }
}
