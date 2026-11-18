import type { KeyValueStore } from "@slop-factory/storage";
import { LoggerProvider, BatchLogRecordProcessor } from "@opentelemetry/sdk-logs";
import { ConsentCheckingExporter } from "./consent_exporter";
import { OTLPLogExporter } from "@opentelemetry/exporter-logs-otlp-http";
import { SeverityNumber, type Logger as OtelApiLogger } from "@opentelemetry/api";
import { resourceFromAttributes } from "@opentelemetry/resources";
import { ATTR_SERVICE_NAME } from "@opentelemetry/semantic-conventions";
import type { Attrs, Logger } from "./logger";
import { ConsoleLogger } from "./console_logger";
import { AttributeFilter, type AttributeAllowlist } from "./attribute_filter";
import { scrubUrlsInText, type RouteTemplateResolver } from "./route_template";
import { SessionId, randomId } from "./session_id";
import { EVENT_NAME_ATTRIBUTE, SESSION_ID_ATTRIBUTE, TIER_ATTRIBUTE, type Tier } from "./tier";

// Records wait in memory for the next batch, and are dropped at this many (and
// whenever an export fails): nothing is queued on disk, which would be storage
// to declare, and a long outage is not worth replaying.
const MAX_QUEUED_RECORDS = 100;
const MAX_RECORDS_PER_EXPORT = 50;

const SEVERITY = {
  info: { number: SeverityNumber.INFO, text: "INFO" },
  error: { number: SeverityNumber.ERROR, text: "ERROR" },
};

export interface OtelLoggerOptions {
  // The resource's service.name, which names the app in the log store.
  serviceName: string;
  // A same-origin path proxied to the app's otel proxy, not the OTel
  // collector's own docker-network address -- the browser can't resolve
  // that, and the proxy adds the rate limiting/service.name enforcement a
  // direct, unauthenticated route to the collector wouldn't have.
  collectorPublicUrl: string;
  // Extra request headers, read at each export -- for a proxy route that
  // authenticates the session and wants its CSRF token. May not throw.
  headers?: () => Record<string, string>;
  // Attribute keys that may leave the browser. Anything else is dropped.
  attributeAllowlist: AttributeAllowlist;
  // Whether the user has consented to analytics. Injected so this package
  // doesn't depend on the consent package. Asked for every analytics record
  // as it is emitted, so a withdrawal takes effect at once. Throwing counts
  // as "no".
  hasAnalyticsConsent: () => boolean;
  // The agreed list of analytics event names. An event not on it is never
  // exported, so a name can't carry free text or identifiers.
  analyticsEvents: readonly string[];
  // Whether the user has consented to interaction recording, which is a
  // purpose of its own and not part of analytics. Absent means never. Asked
  // like hasAnalyticsConsent; throwing counts as "no".
  hasInteractionConsent?: () => boolean;
  // The agreed list of interaction event names, as analyticsEvents is.
  interactionEvents?: readonly string[];
  // The router's template for a pathname, so URLs are exported as templates.
  routeTemplate?: RouteTemplateResolver;
  // Console-warns the first time each attribute key is dropped. The warning
  // names keys only, never values.
  warnOnDrop?: boolean;
  // Where the session id is kept while analytics consent is granted. Absent
  // means the window's sessionStorage; tests pass a MemoryStore.
  sessionStore?: KeyValueStore;
}

// The exporter wants an async factory, so each export reads the headers
// fresh -- a CSRF token changes with the session.
function exportHeaders(headers: (() => Record<string, string>) | undefined) {
  return headers && (async () => headers());
}

// Exports logs to collectorPublicUrl. Every Logger method also always logs
// to the console (delegates to ConsoleLogger), so devtools behave the same
// with or without OTel export, and see every attribute, allowlisted or not.
export class OtelLogger implements Logger {
  private readonly console = new ConsoleLogger();
  private readonly otelLogger: OtelApiLogger;
  private readonly loggerProvider: LoggerProvider;
  private readonly filter: AttributeFilter;
  private readonly sessionId: SessionId;
  private flushing: Promise<void> = Promise.resolve();
  private readonly warnedKeys = new Set<string>();
  private readonly droppedByKey = new Map<string, number>();

  private readonly options: OtelLoggerOptions;
  private readonly analyticsEvents: ReadonlySet<string>;
  private readonly interactionEvents: ReadonlySet<string>;
  // Its own random id, in memory only and never stored, so it needs no
  // consent to keep and cannot be joined to the analytics session id. Forgotten
  // when interaction consent goes, so a later opt-in starts a new one.
  private interactionSessionId: string | undefined;

  constructor(options: OtelLoggerOptions) {
    this.options = options;
    this.analyticsEvents = new Set(options.analyticsEvents);
    this.interactionEvents = new Set(options.interactionEvents ?? []);
    const resource = resourceFromAttributes({ [ATTR_SERVICE_NAME]: options.serviceName });

    this.loggerProvider = new LoggerProvider({
      resource,
      processors: [
        new BatchLogRecordProcessor({
          exporter: new ConsentCheckingExporter(
            new OTLPLogExporter({ url: `${options.collectorPublicUrl}/v1/logs`, headers: exportHeaders(options.headers) }),
            (tier) => this.mayExport(tier),
          ),
          maxQueueSize: MAX_QUEUED_RECORDS,
          maxExportBatchSize: MAX_RECORDS_PER_EXPORT,
        }),
      ],
    });
    this.otelLogger = this.loggerProvider.getLogger(options.serviceName);
    this.filter = new AttributeFilter(options.attributeAllowlist, options.routeTemplate);
    this.sessionId = new SessionId(() => this.analyticsConsented(), options.sessionStore);
  }

  debug(message: string, attrs?: Attrs): void {
    this.console.debug(message, attrs);
  }

  info(message: string, attrs?: Attrs): void {
    this.console.info(message, attrs);
  }

  warn(message: string, attrs?: Attrs): void {
    this.console.warn(message, attrs);
  }

  error(message: string, attrs?: Attrs): void {
    this.console.error(message, attrs);
    this.export("operational", "error", message, attrs);
  }

  event(name: string, attrs?: Attrs): void {
    this.console.event(name, attrs);
    if (!this.analyticsConsented()) return;
    // Offline, an event is dropped rather than held for later.
    if (typeof navigator !== "undefined" && navigator.onLine === false) return;
    if (!this.analyticsEvents.has(name)) {
      this.recordDrops([`event:${name}`]);
      return;
    }
    this.export("analytics", "info", name, attrs, { [EVENT_NAME_ATTRIBUTE]: name });
  }

  interaction(name: string, attrs?: Attrs): void {
    if (!this.interactionConsented()) {
      // Whatever withdrew it, the id goes with it, without waiting to be told.
      this.interactionSessionId = undefined;
      return;
    }
    if (typeof navigator !== "undefined" && navigator.onLine === false) return;
    if (!this.interactionEvents.has(name)) {
      this.recordDrops([`event:${name}`]);
      return;
    }
    this.export("interaction_recording", "info", name, attrs, { [EVENT_NAME_ATTRIBUTE]: name });
  }

  // How many times each attribute key has been dropped by the allowlist.
  droppedAttributeCounts(): ReadonlyMap<string, number> {
    return this.droppedByKey;
  }

  // Sends what is waiting now. For a page that is going away (pagehide, hidden):
  // the transport's keepalive lets the request outlive it.
  //
  // One at a time: the SDK's flush returns at once when another is running,
  // which would leave what arrived since waiting for the next timer, and a
  // closing page may not have one.
  flush(): Promise<void> {
    const run = this.flushing.then(() => this.loggerProvider.forceFlush());
    this.flushing = run.catch(() => {});
    return run;
  }

  // Applies the current consent to what the logger keeps on the device: the
  // session id is cleared when analytics is no longer consented, so a later
  // opt-in starts a new one. Call it when consent changes; every record also
  // checks it.
  syncConsent(): void {
    this.sessionId.sync();
    if (!this.interactionConsented()) this.interactionSessionId = undefined;
  }

  // Flushes and tears down the log exporter -- call on unmount so buffered
  // records aren't dropped.
  async shutdown(): Promise<void> {
    await this.loggerProvider.shutdown();
  }

  private analyticsConsented(): boolean {
    return this.consented(this.options.hasAnalyticsConsent);
  }

  private interactionConsented(): boolean {
    return this.consented(this.options.hasInteractionConsent);
  }

  private consented(check: (() => boolean) | undefined): boolean {
    try {
      return check?.() === true;
    } catch {
      return false;
    }
  }

  // Whether a record of this tier may leave: operational always, the others
  // with the consent of their own purpose. A tier not known here never does.
  private mayExport(tier: Tier): boolean {
    switch (tier) {
      case "operational":
        return true;
      case "analytics":
        return this.analyticsConsented();
      case "interaction_recording":
        return this.interactionConsented();
      default:
        return false;
    }
  }

  private sessionIdFor(tier: Tier): string {
    if (tier !== "interaction_recording") return this.sessionId.get();
    return (this.interactionSessionId ??= randomId());
  }

  // Export is best effort: a failure here must never throw into the code
  // that was only trying to log.
  private export(tier: Tier, level: keyof typeof SEVERITY, body: string, attrs: Attrs | undefined, extra: Record<string, string> = {}): void {
    try {
      this.doExport(tier, level, body, attrs, extra);
    } catch (err) {
      console.warn("logging: export failed", err);
    }
  }

  private doExport(
    tier: Tier,
    level: keyof typeof SEVERITY,
    body: string,
    attrs: Attrs | undefined,
    extra: Record<string, string> = {},
  ): void {
    const { attributes, dropped } = this.filter.filter(tier, attrs);
    this.recordDrops(dropped);

    const { number, text } = SEVERITY[level];
    this.otelLogger.emit({
      body: scrubUrlsInText(body, this.options.routeTemplate),
      severityNumber: number,
      severityText: text,
      // The logger's own attributes go last so nothing above can override
      // the tier.
      attributes: {
        ...attributes,
        ...extra,
        [SESSION_ID_ATTRIBUTE]: this.sessionIdFor(tier),
        [TIER_ATTRIBUTE]: tier,
      },
    });
  }

  private recordDrops(keys: string[]): void {
    for (const key of keys) {
      this.droppedByKey.set(key, (this.droppedByKey.get(key) ?? 0) + 1);
      if (this.options.warnOnDrop && !this.warnedKeys.has(key)) {
        this.warnedKeys.add(key);
        console.warn(`logging: attribute "${key}" is not on the allowlist and is not exported`);
      }
    }
  }
}
