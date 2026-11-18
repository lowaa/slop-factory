import { useState } from "react";
import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import Tabs from "./tabs";

const items = [
  { id: "a", label: "Alpha" },
  { id: "b", label: "Beta" },
  { id: "c", label: "Gamma" },
];

function Demo() {
  const [value, setValue] = useState("a");
  return (
    <Tabs items={items} value={value} onChange={setValue} label="Letters">
      <p>panel {value}</p>
    </Tabs>
  );
}

describe("Tabs", () => {
  it("links the selected tab and its panel", () => {
    render(<Demo />);
    const tab = screen.getByRole("tab", { name: "Alpha" });
    const panel = screen.getByRole("tabpanel");
    expect(tab).toHaveAttribute("aria-selected", "true");
    expect(tab.getAttribute("aria-controls")).toBe(panel.id);
    expect(panel.getAttribute("aria-labelledby")).toBe(tab.id);
    expect(screen.getByRole("tablist", { name: "Letters" })).toBeInTheDocument();
  });

  it("moves with the arrow keys, wrapping, and with Home and End", async () => {
    const user = userEvent.setup();
    render(<Demo />);
    screen.getByRole("tab", { name: "Alpha" }).focus();

    await user.keyboard("{ArrowLeft}");
    expect(screen.getByRole("tab", { name: "Gamma" })).toHaveFocus();
    expect(screen.getByRole("tabpanel")).toHaveTextContent("panel c");

    await user.keyboard("{ArrowRight}");
    expect(screen.getByRole("tab", { name: "Alpha" })).toHaveAttribute("aria-selected", "true");

    await user.keyboard("{End}");
    expect(screen.getByRole("tab", { name: "Gamma" })).toHaveFocus();
    await user.keyboard("{Home}");
    expect(screen.getByRole("tab", { name: "Alpha" })).toHaveFocus();
  });

  it("only the selected tab is in the tab order", () => {
    render(<Demo />);
    expect(screen.getByRole("tab", { name: "Beta" })).toHaveAttribute("tabindex", "-1");
    expect(screen.getByRole("tab", { name: "Alpha" })).toHaveAttribute("tabindex", "0");
  });
});
