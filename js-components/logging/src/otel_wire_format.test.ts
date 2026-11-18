import { createServer, type IncomingHttpHeaders, type Server } from "node:http";
import type { AddressInfo } from "node:net";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { MemoryStore } from "@slop-factory/storage";
import { OtelLogger } from "./otel_logger";

// The other tests swap the OTLP exporter for an in-memory one, so they see
// the SDK's records but not what it serialises. This one keeps the real
// exporter and reads the request it makes, because the server's OTLP proxy
// (go-privacy/otelproxy) parses that JSON and applies its privacy rules to
// it: an SDK upgrade that renames a field or changes how a value is encoded
// would get past the in-memory tests and break the proxy. The transport
// here is the SDK's node one (the environment is node); the browser's is
// covered by the e2e error_reporting spec.

interface Received {
  url: string;
  headers: IncomingHttpHeaders;
  body: unknown;
}

class OtlpReceiver {
  readonly received: Received[] = [];
  private readonly server: Server;
  private waiting: (() => void) | undefined;

  constructor() {
    this.server = createServer((req, res) => {
      const chunks: Buffer[] = [];
      req.on("data", (c: Buffer) => chunks.push(c));
      req.on("end", () => {
        this.received.push({ url: req.url ?? "", headers: req.headers, body: JSON.parse(Buffer.concat(chunks).toString("utf8")) });
        res.writeHead(200, { "content-type": "application/json" }).end("{}");
        this.waiting?.();
      });
    });
  }

  async listen(): Promise<string> {
    await new Promise<void>((resolve) => this.server.listen(0, "127.0.0.1", resolve));
    return `http://127.0.0.1:${(this.server.address() as AddressInfo).port}`;
  }

  // Resolves once a request has arrived, so the test never waits on a timer.
  next(): Promise<void> {
    if (this.received.length > 0) return Promise.resolve();
    return new Promise((resolve) => (this.waiting = resolve));
  }

  close(): Promise<void> {
    return new Promise((resolve) => this.server.close(() => resolve()));
  }
}

// The fields that differ per run.
function normalised(body: unknown): unknown {
  return JSON.parse(JSON.stringify(body), (key, value) => {
    if (key === "timeUnixNano" || key === "observedTimeUnixNano") return typeof value === "string" && /^\d+$/.test(value) ? "<ns>" : value;
    return value;
  });
}

describe("OtelLogger on the wire", () => {
  let receiver: OtlpReceiver;
  let base: string;

  beforeEach(async () => {
    receiver = new OtlpReceiver();
    base = await receiver.listen();
  });

  afterEach(async () => {
    await receiver.close();
  });

  it("posts the OTLP/JSON the server's proxy parses", async () => {
    const logger = new OtelLogger({
      serviceName: "wire-frontend",
      collectorPublicUrl: `${base}/api/otel`,
      headers: () => ({ "X-CSRF-Token": "token-1" }),
      attributeAllowlist: { common: ["error.type", "status", "ratio", "retried", "tags"] },
      analyticsEvents: [],
      hasAnalyticsConsent: () => false,
      sessionStore: new MemoryStore(),
    });

    logger.error("boom", { "error.type": "TypeError", status: 503, ratio: 0.5, retried: true, tags: ["a", "b"], secret: "dropped" });
    const sent = logger.flush();
    await receiver.next();
    await sent;
    await logger.shutdown();

    expect(receiver.received).toHaveLength(1);
    const [req] = receiver.received;
    expect(req.url).toBe("/api/otel/v1/logs");
    expect(req.headers["content-type"]).toBe("application/json");
    expect(req.headers["x-csrf-token"]).toBe("token-1");

    const body = normalised(req.body) as {
      resourceLogs: { scopeLogs: { logRecords: { attributes: { key: string; value: { stringValue?: string } }[] }[] }[] }[];
    };
    const sessionId = body.resourceLogs[0].scopeLogs[0].logRecords[0].attributes.find((a) => a.key === "session.id")?.value.stringValue;
    expect(sessionId).toMatch(/^[0-9a-f]{32}$/);

    expect(body).toEqual({
      resourceLogs: [
        {
          resource: {
            attributes: [{ key: "service.name", value: { stringValue: "wire-frontend" } }],
            droppedAttributesCount: 0,
          },
          scopeLogs: [
            {
              scope: { name: "wire-frontend" },
              logRecords: [
                {
                  timeUnixNano: "<ns>",
                  observedTimeUnixNano: "<ns>",
                  severityNumber: 17,
                  severityText: "ERROR",
                  body: { stringValue: "boom" },
                  attributes: [
                    { key: "error.type", value: { stringValue: "TypeError" } },
                    { key: "status", value: { intValue: 503 } },
                    { key: "ratio", value: { doubleValue: 0.5 } },
                    { key: "retried", value: { boolValue: true } },
                    { key: "tags", value: { arrayValue: { values: [{ stringValue: "a" }, { stringValue: "b" }] } } },
                    { key: "session.id", value: { stringValue: sessionId } },
                    { key: "privacy.tier", value: { stringValue: "operational" } },
                  ],
                  droppedAttributesCount: 0,
                },
              ],
            },
          ],
        },
      ],
    });
  });
});
