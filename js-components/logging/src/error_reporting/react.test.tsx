// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import type { ErrorReporter } from "./error_reporter";
import type { Logger } from "../logger";
import { createRouteErrorFallback, ErrorFallback, ErrorReporterContext, ErrorReportingProvider, ReportingErrorBoundary, ReportingIdentity, RouteErrorReporter, useErrorReporter } from "./react";

function fakeReporter() {
  const reportError = vi.fn();
  return { reporter: { reportError } as unknown as ErrorReporter, reportError };
}

function Crasher(): never {
  throw new Error("boom");
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("ReportingErrorBoundary", () => {
  it("shows the fallback and reports the error with its component stack", () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    const { reporter, reportError } = fakeReporter();

    render(
      <ReportingErrorBoundary reporter={reporter} fallback={<p>fallback</p>}>
        <Crasher />
      </ReportingErrorBoundary>,
    );

    expect(screen.getByText("fallback")).toBeTruthy();
    expect(reportError).toHaveBeenCalledTimes(1);
    const [kind, error, componentStack] = reportError.mock.calls[0];
    expect(kind).toBe("render error");
    expect((error as Error).message).toBe("boom");
    expect(componentStack).toContain("Crasher");
  });

  it("renders its children when nothing fails", () => {
    const { reporter, reportError } = fakeReporter();
    render(
      <ReportingErrorBoundary reporter={reporter} fallback={<p>fallback</p>}>
        <p>fine</p>
      </ReportingErrorBoundary>,
    );
    expect(screen.queryByText("fine")).not.toBeNull();
    expect(reportError).not.toHaveBeenCalled();
  });
});

describe("RouteErrorReporter", () => {
  it("reports the router's error and shows the fallback", () => {
    const { reporter, reportError } = fakeReporter();
    const error = new Error("route failed");

    render(
      <ErrorReporterContext.Provider value={reporter}>
        <RouteErrorReporter error={error} info={{ componentStack: "\n    at Page (file.tsx:1:2)" }}>
          <p>route fallback</p>
        </RouteErrorReporter>
      </ErrorReporterContext.Provider>,
    );

    expect(screen.queryByText("route fallback")).not.toBeNull();
    expect(reportError).toHaveBeenCalledWith("render error", error, "\n    at Page (file.tsx:1:2)");
  });
});

describe("ErrorFallback", () => {
  it("offers a reload that reloads the document", () => {
    const reload = vi.fn();
    vi.stubGlobal("location", { ...window.location, reload });
    render(<ErrorFallback />);

    expect(screen.getByRole("alert").textContent).toContain("Something went wrong");
    fireEvent.click(screen.getByRole("button", { name: "Reload" }));
    expect(reload).toHaveBeenCalledTimes(1);
  });

  it("offers no way to the start unless given one", () => {
    render(<ErrorFallback />);
    expect(screen.queryByRole("link", { name: "Go to start" })).toBeNull();
  });

  it("links to the start as a plain anchor, since the router may be what failed", () => {
    render(<ErrorFallback homeHref="/start" />);
    const link = screen.getByRole("link", { name: "Go to start" });
    expect(link.tagName).toBe("A");
    expect(link.getAttribute("href")).toBe("/start");
  });
});

describe("createRouteErrorFallback", () => {
  it("reports the route's error and shows the fallback with the way home", () => {
    const { reporter, reportError } = fakeReporter();
    const error = new Error("route failed");
    const RouteErrorFallback = createRouteErrorFallback("/home");

    render(
      <ErrorReporterContext.Provider value={reporter}>
        <RouteErrorFallback error={error} info={{ componentStack: "\n    at Page" }} />
      </ErrorReporterContext.Provider>,
    );

    expect(screen.getByRole("alert")).toBeTruthy();
    expect(screen.getByRole("link", { name: "Go to start" }).getAttribute("href")).toBe("/home");
    expect(reportError).toHaveBeenCalledWith("render error", error, "\n    at Page");
  });
});

describe("ErrorReportingProvider", () => {
  function recordingLogger() {
    const errors: { message: string; attrs: Record<string, unknown> }[] = [];
    const logger: Logger = {
      debug() {},
      info() {},
      warn() {},
      event() {},
      error: (message, attrs = {}) => void errors.push({ message, attrs }),
    };
    return { logger, errors };
  }

  function mount(ignoreFailure?: (f: { code?: string }) => boolean) {
    const { logger, errors } = recordingLogger();
    let announce: (f: { method: string; route: string; status?: number }) => void = () => {};
    let subscribers = 0;
    let subscribed = 0;
    const subscribeToFailures = (listener: typeof announce) => {
      subscribers++;
      subscribed++;
      announce = listener;
      return () => void subscribers--;
    };
    let seen: ReturnType<typeof useErrorReporter> | undefined;
    function Probe() {
      seen = useErrorReporter();
      return null;
    }
    vi.stubGlobal("fetch", async () => new Response(JSON.stringify({ version: "t" })));
    const currentRoute = () => "/r";
    const describeFailure = () => undefined;
    const tree = (ignore?: (f: { code?: string }) => boolean) => (
      <ErrorReportingProvider
        logger={logger}
        currentRoute={currentRoute}
        describeFailure={describeFailure}
        subscribeToFailures={subscribeToFailures}
        ignoreFailure={ignore}
        fallback={<p>fallback</p>}
      >
        <ReportingIdentity userId="u1" />
        <Probe />
      </ErrorReportingProvider>
    );
    const view = render(tree(ignoreFailure));
    return { view, errors, rerender: (ignore: (f: { code?: string }) => boolean) => view.rerender(tree(ignore)), subscribes: () => subscribed, announce: (f: Parameters<typeof announce>[0] & { code?: string }) => announce(f), subscribers: () => subscribers, seen: () => seen };
  }

  const flush = () => new Promise((resolve) => setTimeout(resolve, 0));

  it("provides the reporter, hears API failures and global errors, and sets the user id", async () => {
    const m = mount();
    expect(m.seen()).toBeDefined();
    expect(m.subscribers()).toBe(1);

    m.announce({ method: "GET", route: "/api/x/{id}", status: 500 });
    window.dispatchEvent(new ErrorEvent("error", { error: new TypeError("t"), message: "t" }));
    await flush();

    expect(m.errors.map((e) => e.message).sort()).toEqual(["request failed", "uncaught error"]);
    expect(m.errors.every((e) => e.attrs["user.id"] === "u1")).toBe(true);
    expect(m.errors.find((e) => e.message === "uncaught error")?.attrs["http.route"]).toBe("/r");
    expect(m.errors.find((e) => e.message === "request failed")?.attrs["http.route"]).toBe("/api/x/{id}");
  });

  it("does not report a failure the app says to ignore, and reports the rest", async () => {
    const m = mount((f) => f.code === "ignored");

    m.announce({ method: "GET", route: "/a", status: 403, code: "ignored" });
    m.announce({ method: "GET", route: "/b", status: 403, code: "forbidden" });
    await flush();

    expect(m.errors.map((e) => e.attrs["http.route"])).toEqual(["/b"]);
  });

  it("applies the latest ignore predicate without resubscribing", async () => {
    const m = mount(() => false);
    m.rerender(() => true);

    m.announce({ method: "GET", route: "/a", status: 500 });
    await flush();

    expect(m.errors).toEqual([]);
    expect(m.subscribes()).toBe(1);
  });

  it("stops listening when unmounted", () => {
    const added = vi.spyOn(window, "addEventListener");
    const removed = vi.spyOn(window, "removeEventListener");
    const m = mount();
    const handlers = added.mock.calls.filter(([type]) => type === "error" || type === "unhandledrejection");
    expect(handlers.map(([type]) => type).sort()).toEqual(["error", "unhandledrejection"]);

    m.view.unmount();

    expect(m.subscribers()).toBe(0);
    for (const [type, handler] of handlers) expect(removed).toHaveBeenCalledWith(type, handler);
  });
});

describe("ErrorReportingProvider with an appName", () => {
  const logger: Logger = { debug() {}, info() {}, warn() {}, event() {}, error() {} };
  const flush = () => new Promise((resolve) => setTimeout(resolve, 0));

  function mountNamed() {
    return render(
      <ErrorReportingProvider
        logger={logger}
        appName="slop-haus"
        currentRoute={() => "/r"}
        describeFailure={() => undefined}
        subscribeToFailures={() => () => {}}
        fallback={<p>fallback</p>}
      >
        <p>app</p>
      </ErrorReportingProvider>,
    );
  }

  it("shows the update notice once the server's tag differs from the one the tab started with", async () => {
    let version = "t1";
    vi.stubGlobal("fetch", async () => new Response(JSON.stringify({ version })));
    mountNamed();
    await flush();
    expect(screen.queryByRole("status")).toBeNull();

    version = "t2";
    document.dispatchEvent(new Event("visibilitychange"));
    expect(await screen.findByRole("status")).toBeTruthy();
    expect(screen.getByRole("status").textContent).toContain("slop-haus has been updated");
  });

  it("reloads once on a Vite preload error: the guard is set", async () => {
    sessionStorage.clear();
    vi.stubGlobal("fetch", async () => new Response(JSON.stringify({ version: "t1" })));
    vi.spyOn(console, "error").mockImplementation(() => {});
    mountNamed();
    await flush();
    window.dispatchEvent(Object.assign(new Event("vite:preloadError"), { payload: new TypeError("Failed to fetch dynamically imported module") }));
    expect(Object.keys(sessionStorage).some((k) => k.startsWith("chunk-reload:"))).toBe(true);
  });
});
