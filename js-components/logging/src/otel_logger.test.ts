import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { SeverityNumber } from "@opentelemetry/api-logs";
import { ExportResultCode, type ExportResult } from "@opentelemetry/core";
import { InMemoryLogRecordExporter, type ReadableLogRecord } from "@opentelemetry/sdk-logs";
import { ATTR_SERVICE_NAME } from "@opentelemetry/semantic-conventions";
import { BrowserStore, MemoryStore, type KeyValueStore } from "@slop-factory/storage";
import { OtelLogger, type OtelLoggerOptions } from "./otel_logger";

// OTLPLogExporter really performs an HTTP export -- which transport it uses
// (fetch, XHR, node:http) depends on how the bundler resolves the
// package's platform-specific entry points, which is exactly the kind of
// thing that's fragile to fake at the network layer. Swapping it for the
// SDK's own InMemoryLogRecordExporter instead captures real
// ReadableLogRecords through the same BatchLogRecordProcessor pipeline
// OtelLogger actually wires up, so these tests exercise the real
// severity/attribute/resource mapping rather than a hand-rolled guess at
// it.
const { capturedConfigs, instances } = vi.hoisted(() => ({
  capturedConfigs: [] as { url: string; headers?: () => Promise<Record<string, string>> }[],
  instances: [] as InMemoryLogRecordExporter[],
}));

vi.mock("@opentelemetry/exporter-logs-otlp-http", async () => {
  const { InMemoryLogRecordExporter } = await import("@opentelemetry/sdk-logs");
  class FakeOTLPLogExporter extends InMemoryLogRecordExporter {
    constructor(config: { url: string; headers?: () => Promise<Record<string, string>> }) {
      super();
      capturedConfigs.push(config);
      instances.push(this);
    }

    // The real InMemoryLogRecordExporter's shutdown() also calls reset(),
    // clearing everything it just captured -- fine for its own test suite,
    // but OtelLogger.shutdown() is the only flush hook it exposes, so
    // these tests must read getFinishedLogRecords() *after* shutdown has
    // run. Skip the parent's reset so there's still something to read.
    override async shutdown(): Promise<void> {}
  }
  return { OTLPLogExporter: FakeOTLPLogExporter };
});

interface TestOptions {
  collectorPublicUrl?: string;
  serviceName?: string;
  consent?: () => boolean;
  allowlist?: OtelLoggerOptions["attributeAllowlist"];
  routeTemplate?: OtelLoggerOptions["routeTemplate"];
  warnOnDrop?: boolean;
  headers?: () => Record<string, string>;
  events?: readonly string[];
  interactionConsent?: () => boolean;
  interactionEvents?: readonly string[];
  // "default" leaves it to OtelLogger.
  sessionStore?: KeyValueStore | "default";
}

let sessionStore: MemoryStore;

function newLogger(o: TestOptions = {}): OtelLogger {
  return new OtelLogger({
    serviceName: o.serviceName ?? "test-frontend",
    collectorPublicUrl: o.collectorPublicUrl ?? "https://collector.example",
    attributeAllowlist: o.allowlist ?? { common: ["userId", "count", "key", "message", "url"] },
    analyticsEvents: o.events ?? ["button_clicked", "clicked", "e", "before", "after"],
    hasAnalyticsConsent: o.consent ?? (() => true),
    hasInteractionConsent: o.interactionConsent,
    interactionEvents: o.interactionEvents ?? ["pointer_sample", "click"],
    routeTemplate: o.routeTemplate,
    warnOnDrop: o.warnOnDrop,
    headers: o.headers,
    sessionStore: o.sessionStore === "default" ? undefined : (o.sessionStore ?? sessionStore),
  });
}

async function emitAndFlush(emit: (logger: OtelLogger) => void, o: TestOptions = {}): Promise<ReadableLogRecord[]> {
  const logger = newLogger(o);
  emit(logger);
  // shutdown() flushes the BatchLogRecordProcessor before tearing it down,
  // which is what actually hands records to the exporter -- nothing else
  // on OtelLogger's public interface forces that.
  await logger.shutdown();
  return instances.at(-1)!.getFinishedLogRecords();
}

describe("OtelLogger", () => {
  it("gives the exporter a header factory, read at each export", async () => {
    let token = "one";
    newLogger({ headers: () => ({ "X-CSRF-Token": token }) });
    const factory = capturedConfigs.at(-1)!.headers!;
    expect(await factory()).toEqual({ "X-CSRF-Token": "one" });
    token = "two";
    expect(await factory()).toEqual({ "X-CSRF-Token": "two" });
  });

  beforeEach(() => {
    capturedConfigs.length = 0;
    instances.length = 0;
    // The test environment is node, which has no sessionStorage.
    sessionStore = new MemoryStore();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it.each(["debug", "info", "warn", "error"] as const)(
    "%s always logs to the console too, attrs included only when given",
    (level) => {
      const spy = vi.spyOn(console, level).mockImplementation(() => {});
      const logger = newLogger();

      logger[level]("plain message");
      logger[level]("with attrs", { key: "value" });

      expect(spy).toHaveBeenNthCalledWith(1, "plain message");
      expect(spy).toHaveBeenNthCalledWith(2, "with attrs", { key: "value" });
    },
  );

  it("shows devtools every attribute, allowlisted or not", () => {
    const spy = vi.spyOn(console, "error").mockImplementation(() => {});

    newLogger().error("boom", { secret: "x", key: "y" });

    expect(spy).toHaveBeenCalledWith("boom", { secret: "x", key: "y" });
  });

  it("maps error to severity number ERROR / text ERROR", async () => {
    const records = await emitAndFlush((logger) => logger.error("hello"));

    expect(records).toHaveLength(1);
    expect(records[0].severityNumber).toBe(SeverityNumber.ERROR);
    expect(records[0].severityText).toBe("ERROR");
    expect(records[0].body).toBe("hello");
  });

  it("maps event to INFO with the name as body and event.name", async () => {
    const records = await emitAndFlush((logger) => logger.event("button_clicked"));

    expect(records[0].severityNumber).toBe(SeverityNumber.INFO);
    expect(records[0].body).toBe("button_clicked");
    expect(records[0].attributes["event.name"]).toBe("button_clicked");
  });

  it("sets service.name from the constructor argument", async () => {
    const records = await emitAndFlush((logger) => logger.error("hello"), { serviceName: "other-app" });

    expect(records[0].resource.attributes[ATTR_SERVICE_NAME]).toBe("other-app");
  });

  it("posts to <collectorPublicUrl>/v1/logs, not the collector's own address", async () => {
    await emitAndFlush((logger) => logger.error("hello"), { collectorPublicUrl: "https://otelproxy.example/otel" });

    expect(capturedConfigs[0]).toEqual({ url: "https://otelproxy.example/otel/v1/logs" });
  });

  describe("tiers", () => {
    it("exports an operational record whether or not analytics is consented", async () => {
      for (const consent of [true, false]) {
        const records = await emitAndFlush((logger) => logger.error("hello"), { consent: () => consent });

        expect(records).toHaveLength(1);
        expect(records[0].attributes["privacy.tier"]).toBe("operational");
      }
    });

    it("tags an analytics record with the analytics tier", async () => {
      const records = await emitAndFlush((logger) => logger.event("clicked"));

      expect(records[0].attributes["privacy.tier"]).toBe("analytics");
    });

    it("never exports an analytics record without consent", async () => {
      const records = await emitAndFlush((logger) => logger.event("clicked", { key: "v" }), {
        consent: () => false,
      });

      expect(records).toHaveLength(0);
    });

    it("never exports an analytics record when the consent check throws", async () => {
      const records = await emitAndFlush((logger) => logger.event("clicked"), {
        consent: () => {
          throw new Error("consent store unavailable");
        },
      });

      expect(records).toHaveLength(0);
    });

    it("asks for consent per record, so a withdrawal applies at once", async () => {
      let consented = true;
      const logger = newLogger({ consent: () => consented });
      logger.event("before");
      await logger.flush();
      consented = false;
      logger.event("after");
      await logger.shutdown();

      expect(instances.at(-1)!.getFinishedLogRecords().map((r) => r.body)).toEqual(["before"]);
    });

    it("drops an analytics record still waiting to be sent when consent is withdrawn, and keeps an operational one", async () => {
      let consented = true;
      const logger = newLogger({ consent: () => consented });
      logger.event("clicked");
      logger.error("boom");
      consented = false;
      await logger.shutdown();

      expect(instances.at(-1)!.getFinishedLogRecords().map((r) => r.body)).toEqual(["boom"]);
    });

    it("lets a flush finish, and sends nothing, when every record waiting was an event consent has since been withdrawn for", async () => {
      let consented = true;
      const logger = newLogger({ consent: () => consented });
      logger.event("clicked");
      logger.event("e");
      consented = false;

      await expect(logger.flush()).resolves.toBeUndefined();
      await expect(logger.shutdown()).resolves.toBeUndefined();

      expect(instances.at(-1)!.getFinishedLogRecords()).toEqual([]);
    });

    it("drops an event while the browser is offline rather than holding it", async () => {
      vi.stubGlobal("navigator", { onLine: false });

      const records = await emitAndFlush((logger) => {
        logger.event("clicked");
        logger.error("boom");
      });

      expect(records.map((r) => r.body)).toEqual(["boom"]);
    });

    it("sends what arrived while another flush was running, when a second flush is asked for", async () => {
      const real = InMemoryLogRecordExporter.prototype.export;
      let release: (() => void) | undefined;
      let first = true;
      vi.spyOn(InMemoryLogRecordExporter.prototype, "export").mockImplementation(function (
        this: InMemoryLogRecordExporter,
        logs: ReadableLogRecord[],
        callback: (result: ExportResult) => void,
      ) {
        if (first) {
          first = false;
          release = () => real.call(this, logs, callback);
          return;
        }
        return real.call(this, logs, callback);
      });
      const logger = newLogger();
      logger.error("one");
      const a = logger.flush();
      await vi.waitFor(() => expect(release).toBeDefined());
      logger.error("two");
      const b = logger.flush();
      release!();
      await Promise.all([a, b]);

      expect(instances.at(-1)!.getFinishedLogRecords().map((r) => r.body)).toEqual(["one", "two"]);
      await logger.shutdown();
    });

    it("sends what is waiting on flush, without tearing down", async () => {
      const logger = newLogger();
      logger.event("clicked");
      expect(instances.at(-1)!.getFinishedLogRecords()).toHaveLength(0);

      await logger.flush();

      expect(instances.at(-1)!.getFinishedLogRecords()).toHaveLength(1);
      await logger.shutdown();
    });

    it("still shows an unconsented event on the console", () => {
      const spy = vi.spyOn(console, "info").mockImplementation(() => {});

      newLogger({ consent: () => false }).event("clicked", { key: "v" });

      expect(spy).toHaveBeenCalledWith("event: clicked", { key: "v" });
    });

    it.each(["debug", "info", "warn"] as const)("keeps %s on the console and never exports it", async (level) => {
      vi.spyOn(console, level).mockImplementation(() => {});

      const records = await emitAndFlush((logger) => logger[level]("hello"));

      expect(records).toHaveLength(0);
    });

    it("does not let a caller set the tier or session id", async () => {
      const records = await emitAndFlush(
        (logger) =>
          logger.error("hello", { "privacy.tier": "analytics", "session.id": "mine", "event.name": "mine" }),
        { allowlist: { common: ["privacy.tier", "session.id", "event.name"] } },
      );
      const analytics = await emitAndFlush((logger) => logger.event("e", { "privacy.tier": "operational" }), {
        allowlist: { common: ["privacy.tier"] },
      });

      expect(records[0].attributes["privacy.tier"]).toBe("operational");
      expect(records[0].attributes["session.id"]).not.toBe("mine");
      expect(records[0].attributes).not.toHaveProperty("event.name");
      expect(analytics[0].attributes["privacy.tier"]).toBe("analytics");
    });
  });

  describe("interaction tier", () => {
    const interactionAllowlist = { common: ["key"], interaction_recording: ["region"] };

    it("never exports an interaction record without its own consent, whatever analytics says", async () => {
      for (const interactionConsent of [undefined, () => false]) {
        const records = await emitAndFlush((logger) => logger.interaction("click", { region: "footer" }), {
          consent: () => true,
          interactionConsent,
          allowlist: interactionAllowlist,
        });
        expect(records).toEqual([]);
      }
    });

    it("exports it with its consent alone, tagged with its own tier and only its own allowlisted attributes", async () => {
      const records = await emitAndFlush((logger) => logger.interaction("click", { region: "footer", key: "v", secret: "x" }), {
        consent: () => false,
        interactionConsent: () => true,
        allowlist: { common: ["key"], interaction_recording: ["region"], analytics: ["secret"] },
      });

      expect(records).toHaveLength(1);
      expect(records[0].attributes["privacy.tier"]).toBe("interaction_recording");
      expect(records[0].attributes["event.name"]).toBe("click");
      expect(records[0].attributes.region).toBe("footer");
      expect(records[0].attributes.secret).toBeUndefined();
    });

    it("never exports an event that is not on the interaction list, and an analytics event is not one", async () => {
      const records = await emitAndFlush(
        (logger) => {
          logger.interaction("clicked", { region: "footer" });
          logger.interaction("not_agreed");
        },
        { interactionConsent: () => true, allowlist: interactionAllowlist },
      );
      expect(records).toEqual([]);
    });

    it("drops one still waiting when its consent is withdrawn, and keeps an analytics one", async () => {
      let interactionConsented = true;
      const logger = newLogger({ interactionConsent: () => interactionConsented, allowlist: interactionAllowlist });
      logger.interaction("click", { region: "footer" });
      logger.event("clicked");
      interactionConsented = false;
      await logger.shutdown();

      expect(instances.at(-1)!.getFinishedLogRecords().map((r) => r.body)).toEqual(["clicked"]);
    });

    it("takes the analytics records with it only when analytics consent goes, not the other way round", async () => {
      let analytics = true;
      const logger = newLogger({ consent: () => analytics, interactionConsent: () => true, allowlist: interactionAllowlist });
      logger.event("clicked");
      logger.interaction("click", { region: "footer" });
      analytics = false;
      await logger.shutdown();

      expect(instances.at(-1)!.getFinishedLogRecords().map((r) => r.body)).toEqual(["click"]);
    });

    it("is never exported when no interaction events were agreed, or the consent check throws", async () => {
      const none = await emitAndFlush((logger) => logger.interaction("click", { region: "footer" }), {
        interactionConsent: () => true,
        interactionEvents: [],
        allowlist: interactionAllowlist,
      });
      const throwing = await emitAndFlush((logger) => logger.interaction("click", { region: "footer" }), {
        interactionConsent: () => {
          throw new Error("consent store unavailable");
        },
        allowlist: interactionAllowlist,
      });
      expect([...none, ...throwing]).toEqual([]);
    });

    it("drops one while the browser is offline", async () => {
      vi.stubGlobal("navigator", { onLine: false });
      const records = await emitAndFlush((logger) => logger.interaction("click", { region: "footer" }), {
        interactionConsent: () => true,
        allowlist: interactionAllowlist,
      });
      expect(records).toEqual([]);
    });

    it("has its own session id, in memory only, different from the analytics one, and new after a withdrawal", async () => {
      let consented = true;
      const logger = newLogger({ interactionConsent: () => consented, allowlist: interactionAllowlist });
      logger.event("clicked");
      logger.interaction("click", { region: "footer" });
      logger.interaction("click", { region: "footer" });
      await logger.flush();
      consented = false;
      logger.syncConsent();
      consented = true;
      logger.interaction("click", { region: "footer" });
      await logger.shutdown();

      expect(instances.at(-1)!.getFinishedLogRecords()).toHaveLength(4);
      const [analytics, first, second, afterOptIn] = instances.at(-1)!.getFinishedLogRecords().map((r) => r.attributes["session.id"]);
      expect(first).toBe(second);
      expect(first).not.toBe(analytics);
      expect(afterOptIn).not.toBe(first);
      expect(sessionStore.get("slop-factory-logging:session-id")).toBe(analytics);
      expect(sessionStore.get("slop-factory-logging:session-id")).not.toBe(first);
    });
  });

  describe("allowlist", () => {
    it("carries allowlisted attributes onto the exported record", async () => {
      const records = await emitAndFlush((logger) => logger.error("hello", { userId: "u-1", count: 3 }));

      expect(records[0].attributes).toMatchObject({ userId: "u-1", count: 3 });
    });

    it("drops a non-allowlisted attribute", async () => {
      const records = await emitAndFlush((logger) => logger.error("hello", { count: 3, patientName: "Ann" }));

      expect(records[0].attributes).not.toHaveProperty("patientName");
      expect(records[0].attributes).toHaveProperty("count", 3);
    });

    it("drops a non-allowlisted attribute from an analytics record", async () => {
      const records = await emitAndFlush((logger) => logger.event("clicked", { patientName: "Ann" }));

      expect(records[0].attributes).not.toHaveProperty("patientName");
    });

    it("applies a per-tier allowlist: a key allowed for one tier is dropped from the other", async () => {
      const allowlist = { operational: ["opOnly"], analytics: ["anOnly"] };
      const op = await emitAndFlush((l) => l.error("e", { opOnly: 1, anOnly: 2 }), { allowlist });
      const an = await emitAndFlush((l) => l.event("e", { opOnly: 1, anOnly: 2 }), { allowlist });

      expect(op[0].attributes).toMatchObject({ opOnly: 1 });
      expect(op[0].attributes).not.toHaveProperty("anOnly");
      expect(an[0].attributes).toMatchObject({ anOnly: 2 });
      expect(an[0].attributes).not.toHaveProperty("opOnly");
    });

    it("drops an allowlisted key whose value is an object", async () => {
      const records = await emitAndFlush((logger) => logger.error("hello", { key: { nested: "Ann" } }));

      expect(records[0].attributes).not.toHaveProperty("key");
    });

    it("counts drops per key, and console-warns once per key in dev", async () => {
      const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
      vi.spyOn(console, "error").mockImplementation(() => {});
      const logger = newLogger({ warnOnDrop: true });

      logger.error("a", { secret: "s1" });
      logger.error("b", { secret: "s2" });

      expect(logger.droppedAttributeCounts().get("secret")).toBe(2);
      expect(warn).toHaveBeenCalledTimes(1);
      expect(JSON.stringify(warn.mock.calls)).not.toContain("s1");
      await logger.shutdown();
    });

    it("does not warn when warnOnDrop is off", () => {
      const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
      vi.spyOn(console, "error").mockImplementation(() => {});

      newLogger().error("a", { secret: "s1" });

      expect(warn).not.toHaveBeenCalled();
    });
  });

  describe("URLs", () => {
    it("exports a URL attribute as its route template, with no query string or id", async () => {
      const records = await emitAndFlush((logger) =>
        logger.error("hello", { url: "https://app.example/results/123?token=abc#x" }),
      );

      expect(records[0].attributes["http.route"]).toBe("/results/:id");
      expect(records[0].attributes).not.toHaveProperty("url");
      expect(JSON.stringify(records[0].attributes)).not.toMatch(/token|123/);
    });

    it("exports a URL attribute only when its key is allowlisted", async () => {
      const records = await emitAndFlush((logger) => logger.error("hello", { url: "/patients/Ann" }), {
        allowlist: { common: ["message"] },
      });

      expect(records[0].attributes).not.toHaveProperty("http.route");
      expect(JSON.stringify(records[0].attributes)).not.toContain("Ann");
    });

    it.each([
      ["a uuid", "/files/123e4567-e89b-12d3-a456-426614174000/x", "/files/:id/x"],
      ["a long hex id", "/files/0123456789abcdef0123/x", "/files/:id/x"],
      ["an opaque token", "/reset/AbCdEfGhIjKlMnOpQrStUvWxYz/x", "/reset/:id/x"],
    ])("replaces %s in the fallback template", async (_n, path, want) => {
      const records = await emitAndFlush((logger) => logger.error("hello", { url: path }));

      expect(records[0].attributes["http.route"]).toBe(want);
    });

    it("uses the router's template when supplied", async () => {
      const records = await emitAndFlush((logger) => logger.error("hello", { url: "/results/some-slug?x=1" }), {
        routeTemplate: (p) => (p.startsWith("/results/") ? "/results/$slug" : undefined),
      });

      expect(records[0].attributes["http.route"]).toBe("/results/$slug");
    });

    it("never exports an event whose name is not on the agreed list, and counts it", async () => {
      const logger = newLogger();
      logger.event("patient: Ann Smith");
      await logger.shutdown();

      expect(instances.at(-1)!.getFinishedLogRecords()).toHaveLength(0);
      expect(logger.droppedAttributeCounts().get("event:patient: Ann Smith")).toBe(1);
    });

    it("scrubs URLs inside the message body and string attributes", async () => {
      const records = await emitAndFlush((logger) =>
        logger.error("GET https://api.example/users/42?email=a@b.c failed", {
          message: "see https://api.example/users/42?email=a@b.c",
        }),
      );

      expect(records[0].body).toBe("GET /users/:id failed");
      expect(records[0].attributes["message"]).toBe("see /users/:id");
    });
  });

  describe("session id", () => {
    it("is stable within a logger and not derived from any attribute", async () => {
      const records = await emitAndFlush((logger) => {
        logger.error("a", { userId: "u-1" });
        logger.error("b");
      });

      expect(records[0].attributes["session.id"]).toBeTruthy();
      expect(records[0].attributes["session.id"]).toBe(records[1].attributes["session.id"]);
      expect(records[0].attributes["session.id"]).not.toBe("u-1");
    });

    it("is shared by loggers in the same browser session once consented, and differs per session", async () => {
      const a = await emitAndFlush((l) => l.error("a"));
      const b = await emitAndFlush((l) => l.error("b"));
      const c = await emitAndFlush((l) => l.error("c"), { sessionStore: new MemoryStore() });

      expect(a[0].attributes["session.id"]).toBe(b[0].attributes["session.id"]);
      expect(c[0].attributes["session.id"]).not.toBe(a[0].attributes["session.id"]);
    });

    it("keeps the id in memory when the browser refuses storage", async () => {
      const blocked = new BrowserStore({
        storage: () => {
          throw new DOMException("blocked", "SecurityError");
        },
        events: { addEventListener: () => {}, removeEventListener: () => {} },
      });
      const records = await emitAndFlush((l) => {
        l.error("a");
        l.error("b");
      }, { sessionStore: blocked });

      const id = records[0].attributes["session.id"];
      expect(id).toBeTruthy();
      expect(records[1].attributes["session.id"]).toBe(id);
      // The write went to the store, which could only hold it in memory.
      expect(blocked.get("slop-factory-logging:session-id")).toBe(id);
    });

    describe("with the default store", () => {
      let data: Map<string, string>;
      beforeEach(() => {
        data = new Map();
        vi.stubGlobal("sessionStorage", {
          getItem: (k: string) => data.get(k) ?? null,
          setItem: (k: string, v: string) => void data.set(k, v),
          removeItem: (k: string) => void data.delete(k),
        });
      });

      it("is shared through the window's sessionStorage, written once", async () => {
        const setItem = vi.spyOn(sessionStorage, "setItem");
        const a = await emitAndFlush((l) => {
          l.error("a");
          l.error("a again");
        }, { sessionStore: "default" });
        const b = await emitAndFlush((l) => l.error("b"), { sessionStore: "default" });

        expect(a[0].attributes["session.id"]).toBe(data.get("slop-factory-logging:session-id"));
        expect(b[0].attributes["session.id"]).toBe(a[0].attributes["session.id"]);
        expect(setItem).toHaveBeenCalledTimes(1);
      });

      it("adopts an id already in sessionStorage without rewriting it", async () => {
        data.set("slop-factory-logging:session-id", "existing-id");
        const setItem = vi.spyOn(sessionStorage, "setItem");

        const records = await emitAndFlush((l) => l.error("a"), { sessionStore: "default" });

        expect(records[0].attributes["session.id"]).toBe("existing-id");
        expect(setItem).not.toHaveBeenCalled();
      });

      it("writes nothing without consent", async () => {
        await emitAndFlush((l) => l.error("a"), { sessionStore: "default", consent: () => false });

        expect(data.size).toBe(0);
      });

      it("works where there is no sessionStorage at all", async () => {
        vi.unstubAllGlobals();

        const records = await emitAndFlush((l) => l.error("a"), { sessionStore: "default" });

        expect(records[0].attributes["session.id"]).toBeTruthy();
      });
    });

    it("does not need crypto.randomUUID, which insecure http contexts lack", async () => {
      vi.stubGlobal("crypto", { getRandomValues: globalThis.crypto.getRandomValues.bind(globalThis.crypto) });

      const records = await emitAndFlush((l) => l.error("a"));

      expect(records[0].attributes["session.id"]).toMatch(/^[0-9a-f]{32}$/);
    });

    it("is written to sessionStorage only once analytics is consented", async () => {
      await emitAndFlush((logger) => logger.error("a"), { consent: () => false });
      expect(sessionStore.get("slop-factory-logging:session-id")).toBeNull();

      await emitAndFlush((logger) => logger.error("a"), { consent: () => true });
      expect(sessionStore.get("slop-factory-logging:session-id")).toBeTruthy();
    });

    it("is removed from sessionStorage and forgotten when consent is withdrawn, so a later opt-in starts a new one", async () => {
      let consented = true;
      const logger = newLogger({ consent: () => consented });
      logger.error("a");
      const first = sessionStore.get("slop-factory-logging:session-id");
      expect(first).toBeTruthy();

      consented = false;
      logger.syncConsent();
      expect(sessionStore.get("slop-factory-logging:session-id")).toBeNull();

      consented = true;
      logger.error("b");
      expect(sessionStore.get("slop-factory-logging:session-id")).not.toBe(first);
      await logger.shutdown();
    });

    it("clears an id it adopted from storage when consent is withdrawn, even if another logger removed the key", async () => {
      let consented = true;
      const a = newLogger({ consent: () => consented });
      a.error("a");
      const b = newLogger({ consent: () => consented });
      b.error("b");
      consented = false;
      a.syncConsent();
      b.error("after");
      await b.shutdown();
      await a.shutdown();

      const [bBefore, bAfter] = instances.at(-1)!.getFinishedLogRecords();
      expect(bAfter.attributes["session.id"]).not.toBe(bBefore.attributes["session.id"]);
    });

    it("is not carried into records logged after a withdrawal", async () => {
      let consented = true;
      const logger = newLogger({ consent: () => consented });
      logger.error("before");
      consented = false;
      logger.error("after");
      await logger.shutdown();

      const [before, after] = instances.at(-1)!.getFinishedLogRecords();
      expect(after.attributes["session.id"]).not.toBe(before.attributes["session.id"]);
      expect(sessionStore.get("slop-factory-logging:session-id")).toBeNull();
    });
  });

  describe("batching", () => {
    it("queues about 100 records in memory behind an export in progress, drops the rest, and exports no more than 50 at a time", async () => {
      const real = InMemoryLogRecordExporter.prototype.export;
      const batches: number[] = [];
      let releaseFirst: (() => void) | undefined;
      vi.spyOn(InMemoryLogRecordExporter.prototype, "export").mockImplementation(function (
        this: InMemoryLogRecordExporter,
        logs: ReadableLogRecord[],
        callback: (result: ExportResult) => void,
      ) {
        batches.push(logs.length);
        if (batches.length === 1) {
          releaseFirst = () => real.call(this, logs, callback);
          return;
        }
        return real.call(this, logs, callback);
      });
      const logger = newLogger();

      for (let i = 0; i < 300; i++) logger.error(`record ${i}`);
      await vi.waitFor(() => expect(releaseFirst).toBeDefined());
      releaseFirst!();
      await logger.flush();
      await logger.shutdown();

      expect(Math.max(...batches)).toBe(50);
      // The one that was already out, and what the queue held behind it.
      expect(batches.reduce((sum, n) => sum + n, 0)).toBe(150);
    });
  });

  // A collector being unreachable is an ordinary occurrence (dev without
  // the proxy running, a network blip) -- it must not turn every log call
  // into an unhandled rejection or a thrown error in the caller.
  it("does not throw or reject when the export fails", async () => {
    const exportSpy = vi
      .spyOn(InMemoryLogRecordExporter.prototype, "export")
      .mockImplementation((_logs: unknown, resultCallback: (result: ExportResult) => void) => {
        resultCallback({ code: ExportResultCode.FAILED, error: new Error("network down") });
      });

    const logger = newLogger();
    expect(() => logger.error("hello")).not.toThrow();
    await expect(logger.shutdown()).resolves.toBeUndefined();

    // Without this, a broken emit() that silently dropped the record
    // instead of ever calling the exporter would pass the two assertions
    // above just as well.
    expect(exportSpy).toHaveBeenCalled();
  });
});
