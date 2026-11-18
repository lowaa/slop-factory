import { afterEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ErrorReporterContext } from "@slop-factory/logging/error_reporting/react";
import type { ErrorReporter } from "@slop-factory/logging/error_reporting";
import { PanelErrorBoundary } from "./panel_error_boundary";

let broken = true;
function Child() {
  if (broken) throw new Error("kaboom");
  return <p>fine</p>;
}

describe("PanelErrorBoundary", () => {
  afterEach(() => vi.restoreAllMocks());

  it("contains a render error to the panel, logs it, and retries", async () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    const reporter = { reportError: vi.fn() } as unknown as ErrorReporter;
    broken = true;
    render(
      <ErrorReporterContext.Provider value={reporter}>
        <p>sibling</p>
        <PanelErrorBoundary panel="demo">
          <Child />
        </PanelErrorBoundary>
      </ErrorReporterContext.Provider>,
    );
    expect(screen.getByRole("alert")).toHaveTextContent("The demo could not be shown.");
    expect(screen.getByText("sibling")).toBeInTheDocument();
    expect(reporter.reportError).toHaveBeenCalledWith("render error", expect.any(Error), expect.any(String));

    broken = false;
    await userEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(screen.getByText("fine")).toBeInTheDocument();
  });

  it("asks for a reload, not a retry, when the panel's chunk failed to load", () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    const reporter = { reportError: vi.fn() } as unknown as ErrorReporter;
    function Lazy(): never {
      throw new TypeError("Failed to fetch dynamically imported module: https://x/a.js");
    }
    render(
      <ErrorReporterContext.Provider value={reporter}>
        <PanelErrorBoundary panel="demo">
          <Lazy />
        </PanelErrorBoundary>
      </ErrorReporterContext.Provider>,
    );
    expect(screen.getByRole("alert")).toHaveTextContent("reload to continue");
    expect(screen.queryByRole("button", { name: "Try again" })).toBeNull();
    expect(reporter.reportError).toHaveBeenCalledWith("chunk load failed", expect.any(TypeError), undefined);
  });
});
