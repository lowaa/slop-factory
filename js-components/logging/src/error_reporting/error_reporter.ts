import type { Logger } from "../logger";
import type { ApiFailure } from "./api_failures";
import { isChunkLoadError } from "./chunk_recovery";
import { errorType, isBenignError, safeErrorMessage, safeErrorStack, safeStack } from "./safe_error";

// What a failed request's error carries that is safe to say: its status and
// the server's machine-readable code, never its message (a server `detail`
// can echo what the user typed).
export interface RequestFailure {
  status: number;
  code?: string;
}

export interface ErrorReporterOptions {
  logger: Logger;
  // Recognises the app's own API error, so it is reported by status and
  // code. Undefined for anything else.
  describeFailure: (err: unknown) => RequestFailure | undefined;
  // The route template the page is on, from the router.
  currentRoute: () => string | undefined;
  // Tags every record so the right source maps can be found. May resolve
  // late; a record waits for it, briefly, rather than going out untagged.
  buildTag: () => Promise<string>;
  // A newer build the server was seen to serve, once the update notice has
  // seen one. Reported as build.tag.server beside the tab's own tag.
  serverBuildTag?: () => string | undefined;
  userAgent: string;
  now?: () => number;
  // The same error is reported at most once per dedupeWindowMs, and at most
  // maxPerWindow reports go out per rateWindowMs, so one fault in a render
  // loop can't flood the proxy.
  dedupeWindowMs?: number;
  rateWindowMs?: number;
  maxPerWindow?: number;
}

const MAX_TRACKED_ERRORS = 200;

const CODE = /^[a-z0-9_]{1,64}$/;

// Turns crashes and failed requests into operational-tier error records.
// It decides what is safe to say (see safe_error.ts); the logger's allowlist
// is the second gate behind it.
export class ErrorReporter {
  private readonly o: Required<ErrorReporterOptions>;
  private readonly lastSeen = new Map<string, number>();
  // The signed-in user: the operational tier's one stable identifier
  // (go-privacy/README.md §1), set on sign-in and cleared on sign-out.
  private userId: string | undefined;
  private windowStart = 0;
  private inWindow = 0;

  constructor(options: ErrorReporterOptions) {
    this.o = {
      now: Date.now,
      dedupeWindowMs: 60_000,
      rateWindowMs: 60_000,
      maxPerWindow: 10,
      serverBuildTag: () => undefined,
      ...options,
    };
  }

  setUserId(id: string | undefined): void {
    this.userId = id;
  }

  // A thrown value that reached a global handler or an error boundary.
  reportError(kind: string, err: unknown, componentStack?: string | null): void {
    if (isBenignError(err)) return;
    // Told apart from a render error: it is a deploy or the connection, not a bug.
    if (isChunkLoadError(err)) kind = "chunk load failed";
    const type = errorType(err);
    const failure = this.o.describeFailure(err);
    const stack = err instanceof Error ? safeErrorStack(err) : undefined;
    const components = safeStack(componentStack);
    const topFrame = stack?.split("\n")[0] ?? "";
    if (!this.admit(`${kind}|${type}|${topFrame}`)) return;
    this.emit(kind, {
      "error.type": type,
      "error.message": safeErrorMessage(err),
      // An API error's own message is the server's `detail`, which can echo
      // the request; its status and code are the safe summary of it.
      status: failure?.status,
      "error.code": this.safeCode(failure?.code),
      "error.stack": [stack, components && `--- component stack ---\n${components}`].filter(Boolean).join("\n") || undefined,
    });
  }

  // A request that failed. Whether this is worth reporting at all is the
  // caller's call (see api/index.ts); this only keeps what is safe.
  reportApiFailure(f: ApiFailure): void {
    const code = this.safeCode(f.code);
    if (!this.admit(`request|${f.method}|${f.route}|${f.status ?? "network"}|${code ?? ""}`)) return;
    this.emit(f.status === undefined ? "network error" : "request failed", {
      "http.request.method": f.method.toUpperCase(),
      "http.route": f.route,
      status: f.status,
      "error.code": code,
      // A fresh random id for this one request: it finds the server's logs
      // for it, and identifies no one.
      trace_id: f.trace?.traceId,
    });
  }

  private safeCode(code: string | undefined): string | undefined {
    return code !== undefined && CODE.test(code) ? code : undefined;
  }

  private admit(key: string): boolean {
    const now = this.o.now();
    const seen = this.lastSeen.get(key);
    if (seen !== undefined && now - seen < this.o.dedupeWindowMs) return false;
    if (now - this.windowStart >= this.o.rateWindowMs) {
      this.windowStart = now;
      this.inWindow = 0;
    }
    if (this.inWindow >= this.o.maxPerWindow) return false;
    this.inWindow++;
    // Deleted first so the key moves to the end: the map is in the order keys
    // were last seen, which is what evicting the oldest relies on.
    this.lastSeen.delete(key);
    this.lastSeen.set(key, now);
    // Bounded, so a stream of distinct errors can't grow it without limit --
    // not even with a dedupe window longer than the rate window, where no
    // entry has expired yet.
    if (this.lastSeen.size > MAX_TRACKED_ERRORS) {
      for (const [k, t] of this.lastSeen) if (now - t >= this.o.dedupeWindowMs) this.lastSeen.delete(k);
      for (const k of this.lastSeen.keys()) {
        if (this.lastSeen.size <= MAX_TRACKED_ERRORS) break;
        this.lastSeen.delete(k);
      }
    }
    return true;
  }

  private emit(message: string, attrs: Record<string, string | number | undefined>): void {
    // Read now, not after the build tag wait: the page may move on.
    const route = this.o.currentRoute();
    const userId = this.userId;
    void this.resolveBuildTag().then((tag) => {
      const serverTag = this.o.serverBuildTag();
      this.o.logger.error(message, {
        "http.route": route,
        "user.id": userId,
        "user_agent.original": this.o.userAgent,
        "build.tag": tag,
        "build.tag.server": serverTag,
        ...attrs,
      });
    });
  }

  private async resolveBuildTag(): Promise<string> {
    try {
      return await this.o.buildTag();
    } catch {
      return "unknown";
    }
  }
}
