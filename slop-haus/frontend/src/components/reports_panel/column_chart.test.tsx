import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import ColumnChart from "./column_chart";

function heights(): string[] {
  return Array.from(document.querySelectorAll<HTMLElement>(".column-chart__bar")).map((b) => b.style.height);
}

describe("ColumnChart", () => {
  it("draws a bar per value, scaled to the tallest, and none for a missing one", () => {
    render(
      <ColumnChart
        ariaLabel="Done per day"
        series={["p50", "p90"]}
        columns={[
          { key: "a", label: "Mon", values: [50, 100], description: "Mon: p50 50, p90 100" },
          { key: "b", label: "Tue", values: [null, 25], description: "Tue: p90 25" },
          { key: "c", label: "Wed", values: [0, 0], description: "Wed: nothing" },
        ]}
      />,
    );

    expect(screen.getByRole("img", { name: "Done per day" })).toBeInTheDocument();
    expect(heights()).toEqual(["50%", "100%", "0px", "25%", "2%", "2%"]);
    expect(screen.getByTitle("Tue: p90 25")).toBeInTheDocument();
    expect(screen.getByText("Wed")).toBeInTheDocument();
  });

  it("labels only every nth column", () => {
    render(
      <ColumnChart
        ariaLabel="x"
        series={["done"]}
        labelEvery={2}
        columns={["a", "b", "c"].map((k) => ({ key: k, label: k, values: [1], description: k }))}
      />,
    );

    expect(Array.from(document.querySelectorAll(".column-chart__label")).map((l) => l.textContent)).toEqual(["a", "", "c"]);
  });
});
