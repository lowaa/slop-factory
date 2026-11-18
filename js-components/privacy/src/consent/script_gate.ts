// The only sanctioned way to load an optional script (analytics, interaction
// recording, marketing). Nothing loads until the purpose is granted;
// withdrawing it stops any further load and calls the script's teardown.
//
// Adding a vendor is not just a call to this. The app's Content Security
// Policy (`script-src` / `connect-src` in frontend/security_headers.conf)
// must also allow the vendor's origins, the vendor must be declared in the
// data inventory, and a vendor that uses the data for its own purposes
// makes its purpose sale/share (go-privacy/README.md §12). The gate decides
// whether a script loads; the CSP bounds what can.

export type Teardown = () => void;

// What a loader returns: nothing, a teardown, or a promise of either.
// `signal` aborts when consent is withdrawn (or the caller goes away) while
// loading, so a loader can cancel its own request; a loader that finishes
// anyway has its teardown called straight away.
export type ScriptLoader = (signal: AbortSignal) => void | Teardown | Promise<void | Teardown>;

export interface ConsentSource {
  isGranted(purposeId: string): boolean;
  subscribe(listener: () => void): () => void;
}

export interface LoadOptions {
  // Names the script. Two callers with the same id share one load (a
  // component mounted twice, StrictMode's double mount), and the script is
  // torn down when the last of them goes. Without one, every call loads.
  id?: string;
  // Called if the loader or its teardown throws or rejects. A failing vendor
  // script must not break the page, or stop the others.
  onError?: (error: unknown) => void;
}

// "failed" waits for a withdrawal before trying again, so a vendor that is
// down isn't retried on every unrelated consent change.
type Phase = "idle" | "loading" | "loaded" | "failed";

class GatedScript {
  private phase: Phase = "idle";
  private abort: AbortController | null = null;
  private teardown: Teardown | null = null;
  private holders = 1;
  private unsubscribe: () => void;
  private disposed = false;
  private readonly source: ConsentSource;
  readonly purposeId: string;
  private readonly loader: ScriptLoader;
  private readonly onError: (error: unknown) => void;

  constructor(
    source: ConsentSource,
    purposeId: string,
    loader: ScriptLoader,
    onError: (error: unknown) => void,
  ) {
    this.source = source;
    this.purposeId = purposeId;
    this.loader = loader;
    this.onError = onError;
    this.unsubscribe = source.subscribe(() => this.sync());
    this.sync();
  }

  hold(): void {
    this.holders++;
  }

  // True when the last holder has gone and the script is disposed.
  release(): boolean {
    if (--this.holders > 0) return false;
    this.disposed = true;
    this.unsubscribe();
    this.stop();
    return true;
  }

  private sync(): void {
    if (this.disposed) return;
    const granted = this.source.isGranted(this.purposeId);
    if (granted && this.phase === "idle") void this.start();
    else if (!granted && this.phase !== "idle") this.stop();
  }

  private async start(): Promise<void> {
    const abort = new AbortController();
    this.abort = abort;
    this.phase = "loading";
    let teardown: Teardown | void;
    try {
      teardown = await this.loader(abort.signal);
    } catch (e) {
      if (this.abort === abort) this.phase = "failed";
      this.onError(e);
      return;
    }
    // Consent was withdrawn, or the caller went away, while the script was
    // loading. It has loaded anyway, so undo it now.
    if (this.abort !== abort || abort.signal.aborted) {
      this.runTeardown(teardown);
      return;
    }
    this.teardown = typeof teardown === "function" ? teardown : null;
    this.phase = "loaded";
    this.sync();
  }

  private stop(): void {
    const teardown = this.teardown;
    this.abort?.abort();
    this.reset();
    this.runTeardown(teardown);
  }

  private reset(): void {
    this.abort = null;
    this.teardown = null;
    this.phase = "idle";
  }

  private runTeardown(teardown: Teardown | void | null): void {
    if (typeof teardown !== "function") return;
    try {
      teardown();
    } catch (e) {
      this.onError(e);
    }
  }
}

// One gate per consent source, so a script loaded from two places is one
// script.
export class ScriptGate {
  private readonly shared = new Map<string, GatedScript>();
  private readonly source: ConsentSource;

  constructor(source: ConsentSource) {
    this.source = source;
  }

  // Loads `loader` while `purposeId` is granted. Returns a function that
  // stops it: aborts a load in flight, tears the script down, and is safe to
  // call more than once.
  loadWhenConsented(purposeId: string, loader: ScriptLoader, options: LoadOptions = {}): () => void {
    const onError = options.onError ?? (() => undefined);
    const id = options.id;
    let script: GatedScript;
    const existing = id === undefined ? undefined : this.shared.get(id);
    if (existing) {
      if (existing.purposeId !== purposeId) {
        throw new Error(`script "${id}" is already gated on purpose "${existing.purposeId}"`);
      }
      existing.hold();
      script = existing;
    } else {
      script = new GatedScript(this.source, purposeId, loader, onError);
      if (id !== undefined) this.shared.set(id, script);
    }
    let released = false;
    return () => {
      if (released) return;
      released = true;
      if (script.release() && id !== undefined && this.shared.get(id) === script) this.shared.delete(id);
    };
  }
}
