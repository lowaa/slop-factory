// A failed request, from the api client to the exporter, through the
// provider's wiring. A file of its own because openapi-fetch binds the global
// Request when the api module loads, so installFetch must run before the
// api is imported.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, waitFor } from "@testing-library/react";
import { installFetch, jsonResponse, urlOf, type FetchMock } from "@/api/fetch_recorder";

vi.mock("@opentelemetry/exporter-logs-otlp-http", () =>
  import("@slop-factory/logging/error_reporting/testing").then((m) => m.exporterModule()),
);

const SECRET = "Ticket about Jane Doe, hemoglobin 13.5";

describe("a failed request through the api client", () => {
  let fetchMock: FetchMock;
  let api: typeof import("@/api");
  // Loaded after installFetch's module reset, so they are the same instance
  // the logger's mocked exporter registers with.
  let exportedRecords: typeof import("@slop-factory/logging/error_reporting/testing").exportedRecords;
  let exporterConfigs: typeof import("@slop-factory/logging/error_reporting/testing").exporterConfigs;
  let resetExportedRecords: typeof import("@slop-factory/logging/error_reporting/testing").resetExportedRecords;
  let wireOf: typeof import("@slop-factory/logging/error_reporting/testing").wireOf;

  beforeEach(async () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    ({ fetchMock, api } = await installFetch());
    ({ exportedRecords, exporterConfigs, resetExportedRecords, wireOf } = await import("@slop-factory/logging/error_reporting/testing"));
    resetExportedRecords();
  });
  afterEach(() => vi.unstubAllGlobals());

  async function failOnce(status: number) {
    fetchMock.mockImplementation(async (input) => {
      if (urlOf(input).includes("version.json")) return jsonResponse(200, { version: "test-tag" });
      return jsonResponse(status, { detail: `bad ${SECRET}`, code: "internal_error" });
    });
    const { LoggerProvider } = await import("@/context/logger_context");
    const { ErrorReportingProvider } = await import("./error_reporting_context");
    const { unmount } = render(
      <LoggerProvider>
        <ErrorReportingProvider currentRoute={() => undefined}>{null}</ErrorReportingProvider>
      </LoggerProvider>,
    );
    await expect(api.apiClient.getTicket(42)).rejects.toBeInstanceOf(api.ApiError);
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    unmount();
  }

  it("is exported by method and route template, with nothing from the response body or the URL", async () => {
    await failOnce(500);

    await waitFor(() => expect(exportedRecords()).toHaveLength(1));
    expect(exportedRecords()[0].attributes).toMatchObject({
      "privacy.tier": "operational",
      "http.request.method": "GET",
      "http.route": "/api/tickets/{id}",
      status: 500,
      "error.code": "internal_error",
      "build.tag": "test-tag",
    });
    const wire = wireOf(exportedRecords());
    expect(wire).not.toContain("13.5");
    expect(wire).not.toContain("Jane");
    expect(wire).not.toContain("/42");
  });

  it("exports a network failure by method and route template, without a status", async () => {
    fetchMock.mockImplementation(async (input) => {
      if (urlOf(input).includes("version.json")) return jsonResponse(200, { version: "test-tag" });
      throw new TypeError("Failed to fetch");
    });
    const { LoggerProvider } = await import("@/context/logger_context");
    const { ErrorReportingProvider } = await import("./error_reporting_context");
    const { unmount } = render(
      <LoggerProvider>
        <ErrorReportingProvider currentRoute={() => undefined}>{null}</ErrorReportingProvider>
      </LoggerProvider>,
    );
    await expect(api.apiClient.getTicket(42)).rejects.toBeDefined();
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    unmount();

    await waitFor(() => expect(exportedRecords()).toHaveLength(1));
    const attributes = exportedRecords()[0].attributes;
    expect(attributes).toMatchObject({ "http.request.method": "GET", "http.route": "/api/tickets/{id}" });
    expect(attributes).not.toHaveProperty("status");
  });

  it("exports nothing for a 404, an outcome the UI shows", async () => {
    await failOnce(404);
    expect(exportedRecords()).toEqual([]);
  });

  it("sends the exporter's writes with the session's CSRF token, at the same-origin proxy", async () => {
    fetchMock.mockResolvedValueOnce(
      jsonResponse(200, { id: "u", email: "a@b", display_name: "A", tenant_id: "t", is_admin: false, csrf_token: "tok" }),
    );
    await api.apiClient.me();
    const { LoggerProvider } = await import("@/context/logger_context");
    const { unmount } = render(<LoggerProvider>{null}</LoggerProvider>);
    const config = exporterConfigs().at(-1)!;
    unmount();

    expect(config.url).toBe("/api/otel/v1/logs");
    expect(await config.headers!()).toEqual({ [api.CSRF_HEADER]: "tok", "X-Tenant-ID": "t" });
  });

  it("offers the session's CSRF token and tenant for the exporter once signed in, and none before", async () => {
    expect(api.sessionWriteHeaders()).toEqual({});
    fetchMock.mockResolvedValueOnce(
      jsonResponse(200, { id: "u", email: "a@b", display_name: "A", tenant_id: "t", is_admin: false, csrf_token: "tok" }),
    );
    await api.apiClient.me();
    expect(api.sessionWriteHeaders()).toEqual({ [api.CSRF_HEADER]: "tok", "X-Tenant-ID": "t" });
  });
});
