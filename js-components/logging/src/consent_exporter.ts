import { ExportResultCode, type ExportResult } from "@opentelemetry/core";
import type { LogRecordExporter, ReadableLogRecord } from "@opentelemetry/sdk-logs";
import { TIER_ATTRIBUTE, type Tier } from "./tier";

// Wraps the real exporter to ask for consent again as records leave the
// browser, not only as they are emitted: a record can wait in the batch for
// seconds, and a withdrawal in that time must still stop it. Operational
// records need none; every other tier is held to its own purpose.
export class ConsentCheckingExporter implements LogRecordExporter {
  private readonly inner: LogRecordExporter;
  private readonly mayExport: (tier: Tier) => boolean;

  constructor(inner: LogRecordExporter, mayExport: (tier: Tier) => boolean) {
    this.inner = inner;
    this.mayExport = mayExport;
  }

  export(logs: ReadableLogRecord[], resultCallback: (result: ExportResult) => void): void {
    // Asked once per tier, so one batch is not split by a withdrawal that
    // lands halfway through it.
    const allowed = new Map<unknown, boolean>();
    const kept = logs.filter((log) => {
      const tier = log.attributes[TIER_ATTRIBUTE];
      if (tier === "operational") return true;
      if (!allowed.has(tier)) allowed.set(tier, this.mayExport(tier as Tier));
      return allowed.get(tier) === true;
    });
    if (kept.length === 0) {
      resultCallback({ code: ExportResultCode.SUCCESS });
      return;
    }
    this.inner.export(kept, resultCallback);
  }

  shutdown(): Promise<void> {
    return this.inner.shutdown();
  }

  forceFlush(): Promise<void> {
    return this.inner.forceFlush?.() ?? Promise.resolve();
  }
}
