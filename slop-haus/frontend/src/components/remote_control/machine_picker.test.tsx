import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { makeConductor } from "@/test/fixtures";
import MachinePicker from "./machine_picker";
import { usableConductors } from "@/lib/usable_conductors";

const mine = makeConductor({ id: "c-mine", name: "mine" });
const theirs = makeConductor({ id: "c-theirs", name: "theirs", owned_by_me: false });

describe("usableConductors", () => {
  it("offers a person's own enabled remote controls", () => {
    expect(usableConductors({ own: [mine], others: [] })).toEqual([mine]);
  });

  it("never offers another person's remote control, a team owner's view of them included", () => {
    expect(usableConductors({ own: [mine], others: [theirs] })).toEqual([mine]);
  });

  it("does not take a list's word for ownership: a remote control not owned_by_me is dropped even among the own", () => {
    expect(usableConductors({ own: [mine, theirs], others: [] })).toEqual([mine]);
  });

  it("leaves out a disabled remote control", () => {
    const off = makeConductor({ id: "c-off", name: "off", disabled_at: "2026-01-02T00:00:00Z" });
    expect(usableConductors({ own: [mine, off], others: [] })).toEqual([mine]);
  });

  it("leaves out an archived remote control", () => {
    const away = makeConductor({ id: "c-away", name: "away", archived_at: "2026-01-03T00:00:00Z" });
    expect(usableConductors({ own: [mine, away], others: [] })).toEqual([mine]);
  });
});

describe("MachinePicker", () => {
  it("lists the machines it is given and reports the choice", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    const second = makeConductor({ id: "c-2", name: "second" });
    render(<MachinePicker conductors={[mine, second]} value="c-mine" onChange={onChange} />);

    await user.click(screen.getByRole("combobox", { name: "Machine" }));
    expect(screen.getAllByRole("option").map((o) => o.textContent)).toEqual(["mine", "second"]);
    await user.click(screen.getByRole("option", { name: "second" }));

    expect(onChange).toHaveBeenCalledWith("c-2");
  });
});
