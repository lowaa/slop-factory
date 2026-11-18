// The privacy contract of the operational tier, end to end: whatever a crash
// or a failed request carries, a name or a health value in it must not reach
// the log exporter -- the last point before the network. The real logger and
// its allowlist are in the path; only the OTLP exporter is swapped for one
// that keeps what it is handed.
import { afterEach, beforeEach, describe, expect, it, onTestFinished, vi } from "vitest";
import { act, render, screen, waitFor } from "@testing-library/react";
import { exportedRecords, resetExportedRecords, wireOf } from "@slop-factory/logging/error_reporting/testing";
import { apiClient, ApiError } from "@/api";
import { makeConsentStore } from "@/test/consent";
import App from "@/app";
import { LoggerProvider } from "@/context/logger_context";
import { createSlopHausRouter } from "@/routes";
import { makeAuthConfig, makeProfile, makeTicket } from "@/test/fixtures";
import { serveTickets } from "@/test/serve_tickets";
import { ErrorReportingProvider } from "./error_reporting_context";

vi.mock("@opentelemetry/exporter-logs-otlp-http", () =>
  import("@slop-factory/logging/error_reporting/testing").then((m) => m.exporterModule()),
);

const SECRET = "Ticket about Jane Doe, hemoglobin 13.5";

const wire = () => wireOf(exportedRecords());
const attributesOf = (message: string) => exportedRecords().find((r) => r.body === message)?.attributes;

function Crasher({ message }: { message: string }): never {
  throw new Error(message);
}

// A record goes out once the build tag is known, a moment after the error;
// unmounting flushes only what is already emitted.
async function settle() {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
}

beforeEach(() => {
  resetExportedRecords();
  vi.stubGlobal("fetch", async () => new Response(JSON.stringify({ version: "test-tag" })));
  vi.spyOn(console, "error").mockImplementation(() => {});
});

afterEach(() => {
  vi.unstubAllGlobals();
});

function renderApp(path: string) {
  window.history.pushState({}, "", path);
  const router = createSlopHausRouter();
  onTestFinished(() => router.history.destroy());
  return render(<App router={router} consent={makeConsentStore()} />);
}

describe("a render error", () => {
  it("shows the fallback with a reload, and exports the error without its message", async () => {
    const { unmount } = render(
      <LoggerProvider>
        <ErrorReportingProvider currentRoute={() => "/tickets/$id"}>
          <Crasher message={`bad value: ${SECRET}`} />
        </ErrorReportingProvider>
      </LoggerProvider>,
    );

    expect(screen.getByRole("alert")).toHaveTextContent("Something went wrong");
    expect(screen.getByRole("button", { name: "Reload" })).toBeInTheDocument();
    await settle();
    unmount();

    await waitFor(() => expect(exportedRecords()).toHaveLength(1));
    expect(attributesOf("render error")).toMatchObject({
      "privacy.tier": "operational",
      "error.type": "Error",
      "error.message": "message withheld",
      "http.route": "/tickets/$id",
      "build.tag": "test-tag",
    });
    expect(attributesOf("render error")!["error.stack"]).toContain("--- component stack ---");
    expect(wire()).not.toContain("13.5");
    expect(wire()).not.toContain("Jane");
  });
});

describe("a route's render error", () => {
  it("shows the fallback with a link to the start, and exports the error without its message or any user id", async () => {
    // The signed-in user's name is drawn by the header, outside any panel
    // boundary. (A ticket failing to draw in a board column is contained by
    // that column's boundary instead.)
    const profile = makeProfile();
    const ticket = makeTicket();
    Object.defineProperty(profile, "display_name", {
      get() {
        throw new Error(`bad name: ${SECRET}`);
      },
    });
    vi.spyOn(apiClient, "me").mockResolvedValue(profile);
    serveTickets(() => [ticket]);
    for (const m of ["listAgents", "listMembers", "listInvites", "listTenants"] as const) {
      vi.spyOn(apiClient, m).mockResolvedValue([]);
    }
    vi.spyOn(apiClient, "listGitHubWebhooks").mockResolvedValue({ enabled: true, deliveries: [] });
    vi.spyOn(apiClient, "listMemberNames").mockResolvedValue({ members: [], former: [] });
    vi.spyOn(apiClient, "getAuthConfig").mockResolvedValue(makeAuthConfig());

    const { unmount } = renderApp("/");

    // Found by its text: the processing switch's own alert (its setting is not
    // stubbed here) can arrive before the ticket that fails to render does.
    expect(await screen.findByText("Something went wrong")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Go to start" })).toHaveAttribute("href", "/");
    await settle();
    unmount();

    await waitFor(() => expect(exportedRecords().length).toBeGreaterThan(0));
    expect(attributesOf("render error")).toMatchObject({ "error.message": "message withheld" });
    // The server adds the verified user's id; the browser sends none.
    expect(attributesOf("render error")).not.toHaveProperty("user.id");
    expect(wire()).not.toContain(makeProfile().id);
    expect(wire()).not.toContain("13.5");
    expect(wire()).not.toContain("Jane");
  });
});

describe("the global handlers", () => {
  async function runApp(fire: () => void) {
    vi.spyOn(apiClient, "me").mockRejectedValue(new ApiError(401, "no session"));
    vi.spyOn(apiClient, "getAuthConfig").mockResolvedValue(makeAuthConfig());
    const { unmount } = renderApp("/login");
    await screen.findByRole("heading", { name: "Sign in" });
    act(fire);
    await settle();
    unmount();
    await waitFor(() => expect(exportedRecords().length).toBeGreaterThan(0));
  }

  it("exports an uncaught error without its message", async () => {
    await runApp(() =>
      window.dispatchEvent(new ErrorEvent("error", { error: new Error(SECRET), message: `Uncaught Error: ${SECRET}` })),
    );
    expect(attributesOf("uncaught error")).toMatchObject({ "error.type": "Error", "error.message": "message withheld" });
    expect(wire()).not.toContain("13.5");
    expect(wire()).not.toContain("Jane");
  });

  it("exports an unhandled rejection of an error", async () => {
    await runApp(() =>
      window.dispatchEvent(
        Object.assign(new Event("unhandledrejection"), { reason: new TypeError(SECRET), promise: Promise.resolve() }),
      ),
    );
    expect(attributesOf("unhandled rejection")).toMatchObject({ "error.type": "TypeError" });
    expect(wire()).not.toContain("13.5");
  });

  it("exports an unhandled ApiError by status and code, not the server's detail", async () => {
    await runApp(() =>
      window.dispatchEvent(
        Object.assign(new Event("unhandledrejection"), {
          reason: new ApiError(422, `invalid: ${SECRET}`, "validation_failed"),
          promise: Promise.resolve(),
        }),
      ),
    );
    expect(attributesOf("unhandled rejection")).toMatchObject({ status: 422, "error.code": "validation_failed" });
    expect(wire()).not.toContain("13.5");
    expect(wire()).not.toContain("Jane");
  });
});

describe("what the logger's allowlist still stops", () => {
  it("drops an attribute no one allowed", async () => {
    let logger!: ReturnType<typeof import("@/context/logger").useLogger>;
    const { useLogger } = await import("@/context/logger");
    function Grab() {
      logger = useLogger();
      return null;
    }
    const { unmount } = render(
      <LoggerProvider>
        <Grab />
      </LoggerProvider>,
    );
    logger.error("probe", { "form.value": SECRET, "error.message": "x" });
    await settle();
    unmount();

    await waitFor(() => expect(exportedRecords()).toHaveLength(1));
    expect(wire()).not.toContain("13.5");
    expect(attributesOf("probe")).toMatchObject({ "error.message": "x" });
  });
});
