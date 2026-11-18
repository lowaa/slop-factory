import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import HarnessTabs from "./harness_tabs";
import { DEFAULT_HARNESS, HARNESSES, HARNESS_LABELS, harnessById, type HarnessId } from "@/domain/harnesses";

describe("HarnessTabs", () => {
  it("has one tab per registered harness, on the default one first", () => {
    render(<HarnessTabs>{(h) => <p>{h.binary}</p>}</HarnessTabs>);
    expect(screen.getAllByRole("tab").map((t) => t.textContent)).toEqual(HARNESSES.map((h) => h.label));
    const selected = HARNESSES.find((h) => h.id === DEFAULT_HARNESS)!;
    expect(screen.getByRole("tab", { name: selected.label })).toHaveAttribute("aria-selected", "true");
  });

  it.each(HARNESSES)("shows the panel for $label when its tab is chosen", async (harness) => {
    const user = userEvent.setup();
    render(<HarnessTabs>{(h) => <p>{`panel:${h.id}`}</p>}</HarnessTabs>);
    await user.click(screen.getByRole("tab", { name: harness.label }));
    expect(screen.getByRole("tabpanel")).toHaveTextContent(`panel:${harness.id}`);
  });

  it("derives the label table from the registry", () => {
    expect(HARNESS_LABELS).toEqual({ claude: "Claude Code", pi: "pi", codex: "Codex" });
  });

  it("falls back to the default harness for an id it does not know", () => {
    expect(harnessById("nope" as HarnessId).id).toBe(DEFAULT_HARNESS);
  });
});
