import { describe, expect, it } from "vitest";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderSignedIn } from "@/test/render_signed_in";
import { makeCompareResult } from "./compare_fixtures";
import CompareResults from "./compare_results";

describe("CompareResults", () => {
  it("lays the measures out by group with median, spread and n, and never as one score", async () => {
    await renderSignedIn(<CompareResults result={makeCompareResult(6)} />);

    for (const group of ["Cost", "Human guidance", "Quality", "Signals"]) {
      expect(screen.getByRole("table", { name: group })).toBeVisible();
    }
    const cost = within(screen.getByRole("table", { name: "Cost" }));
    expect(cost.getByText("+0.250 USD")).toBeVisible();
    expect(cost.getByText(/IQR -0.750 USD to \+1.25 USD/)).toBeVisible();
    expect(cost.getByRole("cell", { name: "6" })).toBeVisible();
    // A measure with no difference is unknown, not zero.
    expect(within(screen.getByRole("table", { name: "Signals" })).getByText(/unknown/)).toBeVisible();
    expect(screen.getByText(/Baseline: 5 would merge/)).toBeVisible();
    expect(screen.getByText("Runs per ticket: baseline 1, challenger 1.", { exact: false })).toBeVisible();
  });

  it("keeps each challenger's tickets with its own pair", async () => {
    await renderSignedIn(<CompareResults result={makeCompareResult(6, true)} />);

    const fast = within(screen.getByRole("region", { name: "Fast versus Default variant" }));
    await userEvent.click(fast.getByText("Per ticket (1)"));
    expect(fast.getAllByRole("link", { name: "#900" }).length).toBeGreaterThan(0);
    expect(fast.queryByRole("link", { name: "#100" })).toBeNull();
    expect(fast.getAllByRole("link", { name: "challenger attempt 3" }).length).toBe(1);
  });

  it("warns that a small n is too few to trust", async () => {
    await renderSignedIn(<CompareResults result={makeCompareResult(3)} />);

    expect(screen.getByTestId("small-n")).toHaveTextContent("3 tickets: too few to trust a difference");
  });

  it("does not warn at five tickets", async () => {
    await renderSignedIn(<CompareResults result={makeCompareResult(5)} />);

    expect(screen.queryByTestId("small-n")).toBeNull();
  });

  it("flags rows whose runs differed in more than the variant, and says what is not counted", async () => {
    await renderSignedIn(<CompareResults result={makeCompareResult(6)} />);
    await userEvent.click(screen.getByText("Per ticket (6)"));

    expect(screen.getByTestId("run-with-flagged")).toHaveTextContent("1 of these tickets");
    expect(screen.getByText("differs in models")).toBeVisible();
    expect(screen.getByText(/Remote Control guidance is general overhead and is not counted/)).toBeVisible();
  });

  it("links each pair to the ticket and its attempts", async () => {
    await renderSignedIn(<CompareResults result={makeCompareResult(6)} />);
    await userEvent.click(screen.getByText("Per ticket (6)"));

    expect(screen.getByRole("link", { name: "#100" })).toHaveAttribute("href", expect.stringContaining("/tickets/100"));
    const attempt = screen.getAllByRole("link", { name: "challenger attempt 2" })[0];
    expect(attempt).toHaveAttribute("href", expect.stringContaining("/tickets/100/attempts/2"));
    expect(screen.getAllByRole("link", { name: "baseline attempt 1" })[0]).toHaveAttribute(
      "href",
      expect.stringContaining("/tickets/100/attempts/1"),
    );
  });
});
