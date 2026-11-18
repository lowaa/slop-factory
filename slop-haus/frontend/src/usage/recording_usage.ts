import { Usage, type UsageEvent } from "./usage";

// A Usage whose batches are kept for a test to read, with no network behind it.
export class RecordingUsage extends Usage {
  readonly batches: UsageEvent[][] = [];
  flushes = 0;

  constructor() {
    const batches: UsageEvent[][] = [];
    super((events) => batches.push(events), () => true);
    this.batches = batches;
  }

  flush(): void {
    this.flushes++;
    super.flush();
  }

  // Everything sent so far, plus what is still queued (sent as a side effect,
  // without counting as a flush of the page's).
  get events(): UsageEvent[] {
    super.flush();
    return this.batches.flat();
  }

  named(name: string): Record<string, unknown>[] {
    return this.events.filter((e) => e.name === name).map((e) => e.attrs);
  }
}
