// The tag the served image was built as, from /version.json (nginx serves
// it; see the Dockerfile's version stage). Read at run time rather than
// baked into the bundle, so a new tag doesn't rebuild the app -- and so the
// source maps stored under that tag can be found from an error record.
const VERSION_URL = "/version.json";
const TAG = /^[A-Za-z0-9._-]{1,128}$/;
const TIMEOUT_MS = 3000;

export const UNKNOWN_BUILD_TAG = "unknown";

// What /version.json says now. Never throws: a failure is the unknown tag.
export async function fetchBuildTag(): Promise<string> {
  try {
    const response = await fetch(VERSION_URL, { signal: AbortSignal.timeout(TIMEOUT_MS), cache: "no-store" });
    if (!response.ok) return UNKNOWN_BUILD_TAG;
    const body = (await response.json()) as { version?: unknown };
    return typeof body.version === "string" && TAG.test(body.version) ? body.version : UNKNOWN_BUILD_TAG;
  } catch {
    return UNKNOWN_BUILD_TAG;
  }
}

// The build this tab is running. Read once, when the app starts (construct it
// then): index.html and the bundle came from the same image at that moment,
// so that is the build running for the life of the tab. Asking at the first
// error instead would report whatever the server serves by then, which after
// a deploy is not the code that failed.
export class BuildTag {
  private readonly running: Promise<string> = fetchBuildTag();
  private resolved: string | undefined;
  private newer: string | undefined;

  constructor() {
    void this.running.then((tag) => (this.resolved = tag));
  }

  get(): Promise<string> {
    return this.running;
  }

  // The running tag if it has been read yet.
  peek(): string | undefined {
    return this.resolved;
  }

  // A newer tag the server has been seen to serve (see UpdateWatcher).
  noteServerTag(tag: string): void {
    this.newer = tag;
  }

  serverTag(): string | undefined {
    return this.newer;
  }
}
