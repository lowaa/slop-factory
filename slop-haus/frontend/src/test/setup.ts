// Runs before every test file (see vite.config.ts's test.setupFiles).
import "@testing-library/jest-dom/vitest";
import { afterEach, vi } from "vitest";
import { cleanup, configure } from "@testing-library/react";
import { defaultResourceClient } from "@/lib/resource/client";
import { forgetPersonalState } from "@/lib/personal_state";

// Route pages are lazy: the first findBy*/waitFor on one waits for its module
// to be transformed and imported, which under a loaded runner can take longer
// than the 1s default.
configure({ asyncUtilTimeout: 5000 });

// testing-library doesn't auto-cleanup outside of Jest's global afterEach --
// vitest needs this done explicitly, or DOM from one test leaks into the
// next. Restoring mocks the same way keeps vi.spyOn(apiClient, ...) from
// leaking across test files.
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  // Module state above the router (unsent drafts, pending invites), so one
  // test's must not reach the next.
  forgetPersonalState();
  // The cache is module state too: one test's fetched data must not answer the next.
  defaultResourceClient.clear();
});

// The router resets scroll on navigation; jsdom has no layout to scroll and
// logs "Not implemented" for every call.
window.scrollTo = () => {};

// jsdom has no EventSource. An inert one lets anything that opens a stream
// -- the board keeps one open to stay current -- render in any test; the
// tests of the streams themselves stub one that records what they do.
class InertEventSource {
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSED = 2;
  readyState = InertEventSource.CONNECTING;
  onmessage: ((event: MessageEvent) => void) | null = null;
  onerror: ((event: Event) => void) | null = null;
  readonly url: string;
  constructor(url: string) {
    this.url = url;
  }
  close() {
    this.readyState = InertEventSource.CLOSED;
  }
}
globalThis.EventSource ??= InertEventSource as unknown as typeof EventSource;

// jsdom has no layout, so a virtualised list would see a zero-height
// viewport and draw no rows. Give the list's viewport a size, so tests of
// the pages that use it see rows; the list's own tests pin one explicitly.
const JSDOM_VIEWPORT_HEIGHT = 600;
const naturalOffsetHeight = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "offsetHeight");
Object.defineProperty(HTMLElement.prototype, "offsetHeight", {
  configurable: true,
  get() {
    const element = this as HTMLElement;
    return element.classList.contains("virtual-list__viewport")
      ? JSDOM_VIEWPORT_HEIGHT
      : ((naturalOffsetHeight?.get?.call(element) as number | undefined) ?? 0);
  },
});
