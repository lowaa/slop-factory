import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import FilterableDropdown from "./filterable_dropdown";
import type { DropdownOption } from "./option";

const options: DropdownOption[] = [
  { value: "wbc", label: "White cell count", hint: "haem" },
  { value: "hb", label: "Haemoglobin", hint: "haem" },
  { value: "na", label: "Sodium", hint: "lytes", disabled: true },
  { value: "k", label: "Potassium", hint: "lytes" },
];

function renderFilterable(
  props: Partial<React.ComponentProps<typeof FilterableDropdown<string>>> = {},
) {
  const onChange = vi.fn();
  render(
    <FilterableDropdown label="Analyte" value={null} options={options} onChange={onChange} {...props} />,
  );
  return { onChange, input: screen.getByRole("combobox") };
}

function ControlledFilterable() {
  const [value, setValue] = useState<string | null>(null);
  return <FilterableDropdown label="Analyte" value={value} options={options} onChange={setValue} />;
}

function optionLabels() {
  return screen.getAllByRole("option").map((o) => o.textContent);
}

describe("FilterableDropdown", () => {
  // Tab rather than click: click also opens the list, so it would pass
  // with the focus handler removed.
  it("opens on focus", async () => {
    const user = userEvent.setup();
    const { input } = renderFilterable();

    await user.tab();

    expect(screen.getByRole("listbox")).toBeInTheDocument();
    expect(input).toHaveAttribute("aria-expanded", "true");
  });

  it("shows the selected label when closed", () => {
    const { input } = renderFilterable({ value: "hb" });
    expect(input).toHaveValue("Haemoglobin");
  });

  it("is empty when closed with no selection", () => {
    const { input } = renderFilterable();
    expect(input).toHaveValue("");
  });

  // Opening clears the box so the user can type immediately; the selected
  // label moves to the placeholder so the current choice stays readable.
  it("clears the box and shows the selection as placeholder on open", async () => {
    const user = userEvent.setup();
    const { input } = renderFilterable({ value: "hb" });

    await user.click(input);

    expect(input).toHaveValue("");
    expect(input).toHaveAttribute("placeholder", "Haemoglobin");
  });

  it("uses the placeholder prop when nothing is selected", () => {
    const { input } = renderFilterable({ placeholder: "Search analytes" });
    expect(input).toHaveAttribute("placeholder", "Search analytes");
  });

  it("filters options as the user types", async () => {
    const user = userEvent.setup();
    const { input } = renderFilterable();

    await user.click(input);
    await user.type(input, "cell");

    expect(optionLabels()).toEqual(["White cell counthaem"]);
  });

  it("matches case-insensitively", async () => {
    const user = userEvent.setup();
    const { input } = renderFilterable();

    await user.click(input);
    await user.type(input, "SODIUM");

    expect(optionLabels()).toEqual(["Sodiumlytes"]);
  });

  it("matches a substring anywhere, not just a prefix", async () => {
    const user = userEvent.setup();
    const { input } = renderFilterable();

    await user.click(input);
    await user.type(input, "globin");

    expect(optionLabels()).toEqual(["Haemoglobinhaem"]);
  });

  // The hint is searchable, not just decorative -- typing a category
  // narrows to it.
  it("matches against the hint as well as the label", async () => {
    const user = userEvent.setup();
    const { input } = renderFilterable();

    await user.click(input);
    await user.type(input, "lytes");

    expect(optionLabels()).toEqual(["Sodiumlytes", "Potassiumlytes"]);
  });

  it("ignores surrounding whitespace in the filter", async () => {
    const user = userEvent.setup();
    const { input } = renderFilterable();

    await user.click(input);
    await user.type(input, "  cell  ");

    expect(optionLabels()).toEqual(["White cell counthaem"]);
  });

  it("shows the empty message when nothing matches", async () => {
    const user = userEvent.setup();
    const { input } = renderFilterable({ emptyMessage: "No analytes match." });

    await user.click(input);
    await user.type(input, "zzzz");

    expect(screen.getByText("No analytes match.")).toBeInTheDocument();
  });

  it("selects a filtered option on click", async () => {
    const user = userEvent.setup();
    const { onChange, input } = renderFilterable();

    await user.click(input);
    await user.type(input, "pot");
    await user.click(screen.getByRole("option", { name: /Potassium/ }));

    expect(onChange).toHaveBeenCalledWith("k");
    expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
  });

  it("shows the selected label after selecting", async () => {
    const user = userEvent.setup();
    render(<ControlledFilterable />);
    const input = screen.getByRole("combobox");

    await user.click(input);
    await user.type(input, "pot");
    await user.click(screen.getByRole("option", { name: /Potassium/ }));

    expect(input).toHaveValue("Potassium");
  });

  // Reopening to a leftover filter would hide most options with no visible
  // cause, so the filter resets every time the panel opens.
  it("resets the filter when reopened", async () => {
    const user = userEvent.setup();
    const { input } = renderFilterable();

    await user.click(input);
    await user.type(input, "cell");
    await user.keyboard("{Escape}");
    await user.click(input);

    expect(input).toHaveValue("");
    expect(optionLabels()).toHaveLength(4);
  });

  it("does not open when disabled", async () => {
    const user = userEvent.setup();
    const { input } = renderFilterable({ disabled: true });

    await user.click(input);

    expect(input).toBeDisabled();
    expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
  });

  it("closes when focus moves outside it", async () => {
    const user = userEvent.setup();
    render(
      <>
        <FilterableDropdown label="Analyte" value={null} options={options} onChange={vi.fn()} />
        <button type="button">outside</button>
      </>,
    );

    await user.click(screen.getByRole("combobox"));
    expect(screen.getByRole("listbox")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "outside" }));
    expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
  });

  it("exposes the combobox autocomplete affordance", () => {
    const { input } = renderFilterable();
    expect(input).toHaveAttribute("aria-autocomplete", "list");
    expect(input).toHaveAttribute("autocomplete", "off");
  });

  describe("keyboard", () => {
    it("opens on ArrowDown", async () => {
      const user = userEvent.setup();
      const { input } = renderFilterable();
      input.focus();
      await user.keyboard("{Escape}");

      await user.keyboard("{ArrowDown}");

      expect(screen.getByRole("listbox")).toBeInTheDocument();
    });

    it("seeds the first enabled option on open", async () => {
      const user = userEvent.setup();
      const { input } = renderFilterable();

      await user.click(input);

      expect(screen.getByRole("option", { name: /White cell count/ })).toHaveClass(
        "dropdown__option--active",
      );
    });

    it("skips disabled options when navigating the filtered list", async () => {
      const user = userEvent.setup();
      const { input } = renderFilterable();

      await user.click(input);
      await user.type(input, "lytes");
      // Sodium is disabled, so the seed and the first ArrowDown both land
      // on Potassium.
      await user.keyboard("{ArrowDown}");

      expect(screen.getByRole("option", { name: /Potassium/ })).toHaveClass("dropdown__option--active");
    });

    it("selects the active option on Enter", async () => {
      const user = userEvent.setup();
      const { onChange, input } = renderFilterable();

      await user.click(input);
      await user.type(input, "haemo");
      await user.keyboard("{Enter}");

      expect(onChange).toHaveBeenCalledWith("hb");
    });

    it("jumps to the last enabled option on End", async () => {
      const user = userEvent.setup();
      const { input } = renderFilterable();

      await user.click(input);
      await user.keyboard("{End}");

      expect(screen.getByRole("option", { name: /Potassium/ })).toHaveClass("dropdown__option--active");
    });

    // Space is a filter character here, not a selection key -- the
    // opposite of plain Dropdown.
    it("types a space instead of selecting", async () => {
      const user = userEvent.setup();
      const { onChange, input } = renderFilterable();

      await user.click(input);
      await user.type(input, "white cell");

      expect(input).toHaveValue("white cell");
      expect(onChange).not.toHaveBeenCalled();
      expect(optionLabels()).toEqual(["White cell counthaem"]);
    });

    it("closes on Escape without selecting", async () => {
      const user = userEvent.setup();
      const { onChange, input } = renderFilterable();

      await user.click(input);
      await user.keyboard("{Escape}");

      expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
      expect(onChange).not.toHaveBeenCalled();
    });

    // An open panel must swallow Enter, or picking an option would submit
    // the form the dropdown sits in.
    it("does not submit the surrounding form while open", async () => {
      const user = userEvent.setup();
      const onSubmit = vi.fn((e: React.FormEvent) => e.preventDefault());
      render(
        <form onSubmit={onSubmit}>
          <FilterableDropdown label="Analyte" value={null} options={options} onChange={vi.fn()} />
        </form>,
      );

      await user.click(screen.getByRole("combobox"));
      await user.keyboard("{Enter}");

      expect(onSubmit).not.toHaveBeenCalled();
    });

    it("points aria-activedescendant at the active filtered option", async () => {
      const user = userEvent.setup();
      const { input } = renderFilterable();

      await user.click(input);
      await user.type(input, "pot");

      const activeId = input.getAttribute("aria-activedescendant");
      expect(activeId).toBeTruthy();
      expect(screen.getByRole("option", { name: /Potassium/ })).toHaveAttribute("id", activeId!);
    });

    it("omits aria-activedescendant when nothing matches", async () => {
      const user = userEvent.setup();
      const { input } = renderFilterable();

      await user.click(input);
      await user.type(input, "zzzz");

      expect(input).not.toHaveAttribute("aria-activedescendant");
    });
  });

  it("opens on click", async () => {
    const user = userEvent.setup();
    const { input } = renderFilterable();
    input.focus();
    await user.keyboard("{Escape}");

    await user.click(input);

    expect(screen.getByRole("listbox")).toBeInTheDocument();
  });

  describe("caret", () => {
    function caret() {
      const el = document.querySelector(".dropdown__caret");
      if (!el) throw new Error("caret not rendered");
      return el;
    }

    it("opens the list and focuses the filter input", async () => {
      const user = userEvent.setup();
      const { input } = renderFilterable();

      await user.click(caret());

      expect(screen.getByRole("listbox")).toBeInTheDocument();
      expect(input).toHaveFocus();
    });

    // focus() on an already-focused input fires no focus event, so this
    // needs its own open path.
    it("reopens when the input already has focus", async () => {
      const user = userEvent.setup();
      const { input } = renderFilterable();
      input.focus();
      await user.keyboard("{Escape}");

      await user.click(caret());

      expect(screen.getByRole("listbox")).toBeInTheDocument();
    });

    it("reopens on the full list, not a leftover filter", async () => {
      const user = userEvent.setup();
      const { input } = renderFilterable();
      await user.click(input);
      await user.type(input, "cell");
      await user.keyboard("{Escape}");

      await user.click(caret());

      expect(input).toHaveValue("");
      expect(optionLabels()).toHaveLength(options.length);
    });

    // Same seeding as opening from the input, so Enter picks the same row
    // whichever way the list was opened. Starts focused, so the input's own
    // focus handler can't do the seeding for the caret, and with the cursor
    // moved off the first row, so a leftover cursor can't pass for a seed.
    it("highlights the first enabled option", async () => {
      const user = userEvent.setup();
      const { input } = renderFilterable();
      input.focus();
      await user.keyboard("{ArrowDown}{Escape}");

      await user.click(caret());

      const active = input.getAttribute("aria-activedescendant");
      expect(active).not.toBeNull();
      expect(document.getElementById(active ?? "")).toHaveTextContent("White cell count");
    });

    it("closes an open list and keeps focus on the input", async () => {
      const user = userEvent.setup();
      const { input } = renderFilterable();
      await user.click(caret());

      await user.click(caret());

      expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
      expect(input).toHaveFocus();
    });

    it("does nothing when disabled", async () => {
      const user = userEvent.setup();
      const { input } = renderFilterable({ disabled: true });

      await user.click(caret());

      expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
      expect(input).not.toHaveFocus();
    });
  });

  describe("when options change while open", () => {
    it("keeps the cursor on the highlighted option when rows are inserted above it", async () => {
      const user = userEvent.setup();
      const onChange = vi.fn();
      const { rerender } = render(
        <FilterableDropdown label="Analyte" value={null} options={options} onChange={onChange} />,
      );

      await user.click(screen.getByRole("combobox"));
      await user.keyboard("{ArrowDown}");
      rerender(
        <FilterableDropdown
          label="Analyte"
          value={null}
          options={[{ value: "cr", label: "Creatinine" }, ...options]}
          onChange={onChange}
        />,
      );
      await user.keyboard("{Enter}");

      expect(onChange).toHaveBeenCalledWith("hb");
    });

    // Sodium goes too: it's disabled, so an index-based cursor landing on
    // it would also commit nothing and the test couldn't tell the two
    // implementations apart. With it gone, a stale index hits Potassium.
    const withoutHighlighted = options.filter((o) => o.value !== "hb" && o.value !== "na");

    it("commits nothing when the highlighted option disappears", async () => {
      const user = userEvent.setup();
      const onChange = vi.fn();
      const { rerender } = render(
        <FilterableDropdown label="Analyte" value={null} options={options} onChange={onChange} />,
      );

      await user.click(screen.getByRole("combobox"));
      await user.keyboard("{ArrowDown}");
      rerender(
        <FilterableDropdown
          label="Analyte"
          value={null}
          options={withoutHighlighted}
          onChange={onChange}
        />,
      );
      await user.keyboard("{Enter}");

      expect(onChange).not.toHaveBeenCalled();
    });
  });

  it("does not move the cursor onto a disabled option on hover", async () => {
    const user = userEvent.setup();
    const { onChange, input } = renderFilterable();

    await user.click(input);
    await user.hover(screen.getByRole("option", { name: /Sodium/ }));
    await user.keyboard("{Enter}");

    expect(onChange).toHaveBeenCalledWith("wbc");
  });

  it("keeps the label as the accessible name when it is visually hidden", () => {
    renderFilterable({ labelHidden: true });

    expect(screen.getByRole("combobox", { name: "Analyte" })).toBeInTheDocument();
    expect(screen.getByText("Analyte")).toHaveClass("dropdown__label--hidden");
  });

  describe("required", () => {
    function renderInForm(props: Partial<React.ComponentProps<typeof FilterableDropdown<string>>>) {
      const onSubmit = vi.fn((e: React.FormEvent) => e.preventDefault());
      render(
        <form onSubmit={onSubmit}>
          <FilterableDropdown label="Analyte" value={null} options={options} onChange={vi.fn()} {...props} />
          <button type="submit">Save</button>
        </form>,
      );
      return { onSubmit };
    }

    // Typing a filter puts text in the visible box without selecting
    // anything, which is exactly why validation can't key off that box.
    it("blocks submit while nothing is selected, even with filter text typed", async () => {
      const user = userEvent.setup();
      const { onSubmit } = renderInForm({ required: true });

      await user.type(screen.getByRole("combobox"), "haem");
      await user.keyboard("{Escape}");
      await user.click(screen.getByRole("button", { name: "Save" }));

      expect(onSubmit).not.toHaveBeenCalled();
      expect(screen.getByRole("combobox")).toHaveAttribute("aria-required", "true");
    });

    it("blocks submit when the selected value is no longer among the options", async () => {
      const user = userEvent.setup();
      const { onSubmit } = renderInForm({ required: true, value: "retired" });

      await user.click(screen.getByRole("button", { name: "Save" }));

      expect(onSubmit).not.toHaveBeenCalled();
    });

    it("allows submit once an option is selected", async () => {
      const user = userEvent.setup();
      const { onSubmit } = renderInForm({ required: true, value: "hb" });

      await user.click(screen.getByRole("button", { name: "Save" }));

      expect(onSubmit).toHaveBeenCalledTimes(1);
    });

    it("exempts a disabled dropdown, as the browser does a disabled <select>", async () => {
      const user = userEvent.setup();
      const { onSubmit } = renderInForm({ required: true, disabled: true });

      await user.click(screen.getByRole("button", { name: "Save" }));

      expect(onSubmit).toHaveBeenCalledTimes(1);
    });

    it("leaves the form unconstrained when not required", async () => {
      const user = userEvent.setup();
      const { onSubmit } = renderInForm({});

      await user.click(screen.getByRole("button", { name: "Save" }));

      expect(onSubmit).toHaveBeenCalledTimes(1);
    });

    it("hands focus to the filter input when the validation input is focused", () => {
      const { container } = render(
        <FilterableDropdown label="Analyte" value={null} options={options} onChange={vi.fn()} required />,
      );

      container.querySelector<HTMLInputElement>(".dropdown__validation")!.focus();

      expect(screen.getByRole("combobox")).toHaveFocus();
    });
  });
});
