// Test support: stands in for the OTLP exporter, keeping what the logger
// hands it so a test can read exactly what would have left the browser.
// Used as `vi.mock("@opentelemetry/exporter-logs-otlp-http", () => import("./recording_exporter").then((m) => m.exporterModule()))`.
import { InMemoryLogRecordExporter, type ReadableLogRecord } from "@opentelemetry/sdk-logs";

// Held on globalThis: a test that resets the module registry (to load an
// app's api client afresh) would otherwise leave the mocked exporter writing
// to one copy of this module while the test reads another.
const STATE = Symbol.for("slop-factory.logging.recording-exporter");
const state = ((globalThis as Record<symbol, unknown>)[STATE] ??= {
  exporters: [] as InMemoryLogRecordExporter[],
  configs: [] as ExporterConfig[],
}) as { exporters: InMemoryLogRecordExporter[]; configs: ExporterConfig[] };
const { exporters, configs } = state;

// What the logger configured its exporter with: where it posts and which
// headers it sends.
export interface ExporterConfig {
  url?: string;
  headers?: () => Promise<Record<string, string>>;
}

class RecordingExporter extends InMemoryLogRecordExporter {
  constructor(config: ExporterConfig = {}) {
    super();
    exporters.push(this);
    configs.push(config);
  }
  // The parent clears what it holds on shutdown, which is when the logger
  // flushes to it.
  override async shutdown(): Promise<void> {}
}

export function exporterModule() {
  return { OTLPLogExporter: RecordingExporter };
}

export function exportedRecords(): ReadableLogRecord[] {
  return exporters.flatMap((e) => e.getFinishedLogRecords());
}

export function exporterConfigs(): ExporterConfig[] {
  return configs;
}

export function resetExportedRecords(): void {
  exporters.length = 0;
  configs.length = 0;
}

// Everything that would go out: bodies, attributes and resource.
export function wireOf(records: ReadableLogRecord[]): string {
  return JSON.stringify(records.map((r) => ({ body: r.body, attributes: r.attributes, resource: r.resource.attributes })));
}
