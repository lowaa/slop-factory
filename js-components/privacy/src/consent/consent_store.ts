import { createFetchConsentApi } from "./consent_api";
import { ScriptGate, type LoadOptions, type ScriptLoader } from "./script_gate";
import { applyGpc, readGpcSignal } from "./gpc";
import {
  ConsentApiError,
  type ConsentApi,
  type ConsentState,
  type Decision,
  type DecisionSource,
  type PurposeState,
} from "./types";

export interface ConsentSnapshot {
  // "loading" until the server has answered once. Nothing is granted until
  // then, so a script gated on consent can't load ahead of the answer.
  status: "loading" | "ready" | "error";
  purposes: readonly PurposeState[];
  signedIn: boolean;
  gpc: { signalled: boolean; honoured: boolean };
  saving: boolean;
  // The last failed load or decision. Cleared by the next attempt.
  error: ConsentApiError | null;
  // Bumps on every decision that reached the server, so a live region can
  // announce it.
  savedCount: number;
}

export interface ConsentStoreOptions {
  api?: ConsentApi;
  // Names the cross-tab lock and channel; one per app.
  name?: string;
  gpcSignal?: () => boolean;
  // Both default to the browser's, when it has them. Tests inject their own.
  locks?: Pick<LockManager, "request"> | null;
  createChannel?: ((name: string) => BroadcastChannel) | null;
}

const CODES_THAT_MEAN_STALE: ReadonlySet<string> = new Set(["consent_changed", "document_changed", "gpc_active"]);

export function isCookieCategory(p: PurposeState): boolean {
  return p.kind === "cookie_category";
}

// The purposes the banner asks about: processing purposes have their own
// prompts, and necessary ones are never asked.
export function purposesNeedingBanner(purposes: readonly PurposeState[]): PurposeState[] {
  return purposes.filter((p) => isCookieCategory(p) && !p.necessary && p.needsDecision);
}

export class ConsentStore {
  private snapshot: ConsentSnapshot;
  private readonly listeners = new Set<() => void>();
  private readonly api: ConsentApi;
  private readonly name: string;
  private readonly gpcSignal: () => boolean;
  private readonly locks: Pick<LockManager, "request"> | null;
  private readonly createChannel: ((name: string) => BroadcastChannel) | null;
  private channel: BroadcastChannel | null = null;
  private state: ConsentState | null = null;
  private loading: Promise<void> | null = null;
  // Counts the start and end of each decision. A read that overlapped one is
  // dropped: it may have been answered before the decision landed, and would
  // put the old state back. The decision's own answer is the fresher one.
  private writeEpoch = 0;
  private pendingWrites = 0;
  private writes: Promise<unknown> = Promise.resolve();
  private readonly gate = new ScriptGate(this);

  constructor(options: ConsentStoreOptions = {}) {
    this.api = options.api ?? createFetchConsentApi();
    this.name = options.name ?? "slop-factory-consent";
    this.gpcSignal = options.gpcSignal ?? (() => readGpcSignal());
    this.locks =
      options.locks !== undefined ? options.locks : typeof navigator !== "undefined" ? (navigator.locks ?? null) : null;
    this.createChannel =
      options.createChannel !== undefined
        ? options.createChannel
        : typeof BroadcastChannel !== "undefined"
          ? (n) => new BroadcastChannel(n)
          : null;
    this.snapshot = {
      status: "loading",
      purposes: [],
      signedIn: false,
      gpc: { signalled: this.gpcSignal(), honoured: false },
      saving: false,
      error: null,
      savedCount: 0,
    };
  }

  subscribe = (listener: () => void): (() => void) => {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  };

  getSnapshot = (): ConsentSnapshot => this.snapshot;

  isGranted(purposeId: string): boolean {
    return this.snapshot.purposes.some((p) => p.id === purposeId && p.granted);
  }

  // See ScriptGate. Returns a function that stops the script.
  loadWhenConsented(purposeId: string, loader: ScriptLoader, options?: LoadOptions): () => void {
    return this.gate.loadWhenConsented(purposeId, loader, options);
  }

  // A function for code outside React that asks per call, such as
  // @slop-factory/logging's `hasAnalyticsConsent`, so a withdrawal
  // applies to the very next record.
  check(purposeId: string): () => boolean {
    return () => this.isGranted(purposeId);
  }

  // Starts listening for decisions made in other tabs, and loads. Returns
  // the function that stops listening. Safe to call twice (React StrictMode
  // mounts, unmounts and mounts again); the store itself outlives it.
  start(): () => void {
    this.channel ??= this.createChannel?.(this.name) ?? null;
    const channel = this.channel;
    if (channel) {
      channel.onmessage = () => {
        void this.refresh();
      };
    }
    void this.load();
    return () => {
      channel?.close();
      if (this.channel === channel) this.channel = null;
    };
  }

  // Reads the state, unless a read is already in flight (React StrictMode
  // mounts twice): that one is the answer.
  load(): Promise<void> {
    return this.loading ?? this.queueRead();
  }

  // A read that starts after this call. For when something has just said the
  // state moved on (another tab decided, or a write was refused as stale): a
  // read already in flight may have been answered before that, so this waits
  // for it and reads again rather than joining it.
  refresh(): Promise<void> {
    return this.queueRead();
  }

  private queueRead(): Promise<void> {
    const tail: Promise<void> = (this.loading ?? Promise.resolve()).then(() => this.read()).finally(() => {
      if (this.loading === tail) this.loading = null;
    });
    this.loading = tail;
    return tail;
  }

  private async read(): Promise<void> {
    const epoch = this.writeEpoch;
    try {
      const state = await this.api.get();
      if (epoch === this.writeEpoch) this.apply(state);
    } catch (e) {
      if (epoch !== this.writeEpoch) return;
      this.set({ status: this.state ? "ready" : "error", error: toError(e) });
    }
  }

  // Decisions go to the server only: the state shown is what it answers
  // with, so the page can never claim a choice the server didn't take.
  // Returns whether the choice was recorded.
  decide(source: DecisionSource, decisions: readonly Decision[]): Promise<boolean> {
    if (decisions.length === 0) return Promise.resolve(false);
    this.pendingWrites++;
    this.set({ saving: true, error: null });
    const run = () => this.write(source, decisions);
    // One at a time within this tab, and one at a time across tabs: the
    // anonymous cookie is last-write-wins in the browser, so two PUTs in
    // flight could leave the older choice's Set-Cookie last. A browser
    // without Web Locks keeps only the in-tab ordering: the residual risk
    // go-privacy/README.md §5 accepts, and signed-in decisions are protected
    // by the server's stale-write check regardless.
    const queued = this.writes.then(
      () => (this.locks ? this.locks.request(this.name, run) : run()),
      () => (this.locks ? this.locks.request(this.name, run) : run()),
    );
    this.writes = queued.catch(() => undefined);
    return queued.finally(() => {
      this.pendingWrites--;
      this.set({ saving: this.pendingWrites > 0 });
    });
  }

  private async write(source: DecisionSource, decisions: readonly Decision[]): Promise<boolean> {
    const current = this.state;
    if (!current) {
      this.set({ error: new ConsentApiError("unknown", 0) });
      return false;
    }
    const known = new Set(current.purposes.map((p) => p.id));
    const wanted = decisions.filter((d) => known.has(d.purpose));
    if (wanted.length === 0) return false;
    this.writeEpoch++;
    try {
      const state = await this.api.put(source, wanted, current);
      this.apply(state);
      this.set({ savedCount: this.snapshot.savedCount + 1 });
      this.channel?.postMessage("changed");
      return true;
    } catch (e) {
      const err = toError(e);
      this.set({ error: err });
      // The state this decision was made on is out of date; show the new one.
      if (CODES_THAT_MEAN_STALE.has(err.code)) {
        await this.refresh();
        this.set({ error: err });
      }
      return false;
    } finally {
      this.writeEpoch++;
    }
  }

  // Decides every non-necessary cookie purpose at once, as the banner's
  // Accept all and Reject all do. A purpose already decided that way isn't
  // sent again (it would only add an event), and one Global Privacy Control
  // is denying is denied even on Accept all: granting it would be refused.
  decideAll(source: DecisionSource, grant: boolean): Promise<boolean> {
    const decisions = this.snapshot.purposes
      .filter((p) => isCookieCategory(p) && !p.necessary)
      .map((p) => ({ purpose: p.id, granted: grant && !p.gpcLocked, p }))
      .filter(({ granted, p }) => p.needsDecision || p.granted !== granted)
      .map(({ purpose, granted }) => ({ purpose, granted }));
    if (decisions.length === 0) return Promise.resolve(true);
    return this.decide(source, decisions);
  }

  private apply(state: ConsentState): void {
    this.state = state;
    this.set({
      status: "ready",
      purposes: state.purposes.map(applyGpc),
      signedIn: state.signedIn,
      gpc: { signalled: this.gpcSignal(), honoured: state.gpcHonoured },
      error: null,
    });
  }

  private set(patch: Partial<ConsentSnapshot>): void {
    this.snapshot = { ...this.snapshot, ...patch };
    for (const l of [...this.listeners]) l();
  }
}

function toError(e: unknown): ConsentApiError {
  return e instanceof ConsentApiError ? e : new ConsentApiError("unknown", 0);
}
