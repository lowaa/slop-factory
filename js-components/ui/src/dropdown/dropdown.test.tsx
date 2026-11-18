import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import Dropdown from "./dropdown";
import type { DropdownOption } from "./option";

const options: DropdownOption[] = [
  { value: "wbc", label: "White cell count", hint: "haem" },
  { value: "hb", label: "Haemoglobin", hint: "haem" },
  { value: "na", label: "Sodium", disabled: true },
  { value: "k", label: "Potassium" },
];

function renderDropdown(props: Partial<React.ComponentProps<typeof Dropdown<string>>> = {}) {
  const onChange = vi.fn();
  render(<Dropdown label="Analyte" value={null} options={options} onChange={onChange} {...props} />);
  return { onChange, trigger: screen.getByRole("combobox") };
}

// Drives the real controlled-component contract rather than asserting on a
// spy, so "selecting updates the display" is covered end to end.
function ControlledDropdown() {
  const [value, setValue] = useState<string | null>(null);
  return <Dropdown label="Analyte" value={value} options={options} onChange={setValue} />;
}

describe("Dropdown", () => {
  it("shows the placeholder when nothing is selected", () => {
    renderDropdown();
    expect(screen.getByText("Choose…")).toHaveClass("dropdown__value--placeholder");
  });

  it("shows the selected option's label", () => {
    renderDropdown({ value: "hb" });
    expect(screen.getByText("Haemoglobin")).not.toHaveClass("dropdown__value--placeholder");
  });

  it("renders a custom placeholder", () => {
    renderDropdown({ placeholder: "Pick an analyte" });
    expect(screen.getByText("Pick an analyte")).toBeInTheDocument();
  });

  it("passes aria-describedby to the combobox", () => {
    const { trigger } = renderDropdown({ "aria-describedby": "help" });
    expect(trigger).toHaveAttribute("aria-describedby", "help");
  });

  it("keeps the panel closed until opened", () => {
    const { trigger } = renderDropdown();
    expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
    expect(trigger).toHaveAttribute("aria-expanded", "false");
  });

  it("opens on click and closes on a second click", async () => {
    const user = userEvent.setup();
    const { trigger } = renderDropdown();

    await user.click(trigger);
    expect(screen.getByRole("listbox")).toBeInTheDocument();
    expect(trigger).toHaveAttribute("aria-expanded", "true");

    await user.click(trigger);
    expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
  });

  it("selects an option on click", async () => {
    const user = userEvent.setup();
    const { onChange, trigger } = renderDropdown();

    await user.click(trigger);
    await user.click(screen.getByRole("option", { name: /Potassium/ }));

    expect(onChange).toHaveBeenCalledWith("k");
    expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
  });

  it("updates the displayed label after selecting", async () => {
    const user = userEvent.setup();
    render(<ControlledDropdown />);

    await user.click(screen.getByRole("combobox"));
    await user.click(screen.getByRole("option", { name: /Haemoglobin/ }));

    expect(screen.getByText("Haemoglobin")).toBeInTheDocument();
    expect(screen.queryByText("Choose…")).not.toBeInTheDocument();
  });

  it("ignores clicks on a disabled option", async () => {
    const user = userEvent.setup();
    const { onChange, trigger } = renderDropdown();

    await user.click(trigger);
    await user.click(screen.getByRole("option", { name: /Sodium/ }));

    expect(onChange).not.toHaveBeenCalled();
    expect(screen.getByRole("listbox")).toBeInTheDocument();
  });

  it("marks the selected option with aria-selected", async () => {
    const user = userEvent.setup();
    const { trigger } = renderDropdown({ value: "hb" });

    await user.click(trigger);

    expect(screen.getByRole("option", { name: /Haemoglobin/ })).toHaveAttribute("aria-selected", "true");
    expect(screen.getByRole("option", { name: /Potassium/ })).toHaveAttribute("aria-selected", "false");
  });

  it("marks a disabled option with aria-disabled", async () => {
    const user = userEvent.setup();
    const { trigger } = renderDropdown();

    await user.click(trigger);

    expect(screen.getByRole("option", { name: /Sodium/ })).toHaveAttribute("aria-disabled", "true");
    expect(screen.getByRole("option", { name: /Potassium/ })).not.toHaveAttribute("aria-disabled");
  });

  it("renders option hints", async () => {
    const user = userEvent.setup();
    const { trigger } = renderDropdown();

    await user.click(trigger);

    expect(screen.getAllByText("haem")).toHaveLength(2);
  });

  it("draws an option's leading decoration in the list and on the trigger, outside its name", async () => {
    const user = userEvent.setup();
    const decorated = [{ value: "a", label: "Alpha", leading: <i data-testid="lead">A</i> }];
    render(<Dropdown label="Pick" value="a" options={decorated} onChange={() => {}} />);

    expect(screen.getAllByTestId("lead")).toHaveLength(1);
    await user.click(screen.getByRole("combobox", { name: "Pick" }));

    expect(screen.getAllByTestId("lead")).toHaveLength(2);
    expect(screen.getByRole("option", { name: "Alpha" })).toBeInTheDocument();
  });

  it("shows the empty message when there are no options", async () => {
    const user = userEvent.setup();
    const { trigger } = renderDropdown({ options: [], emptyMessage: "No analytes configured." });

    await user.click(trigger);

    expect(screen.getByText("No analytes configured.")).toBeInTheDocument();
  });

  it("does not open when disabled", async () => {
    const user = userEvent.setup();
    const { trigger } = renderDropdown({ disabled: true });

    await user.click(trigger);

    expect(trigger).toBeDisabled();
    expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
  });

  it("closes when a pointer goes down outside it", async () => {
    const user = userEvent.setup();
    render(
      <>
        <Dropdown label="Analyte" value={null} options={options} onChange={vi.fn()} />
        <button type="button">outside</button>
      </>,
    );

    await user.click(screen.getByRole("combobox"));
    expect(screen.getByRole("listbox")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "outside" }));
    expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
  });

  describe("keyboard", () => {
    it("opens on ArrowDown, Enter and Space", async () => {
      const user = userEvent.setup();
      for (const key of ["{ArrowDown}", "{Enter}", "[Space]"]) {
        const { unmount } = render(
          <Dropdown label="Analyte" value={null} options={options} onChange={vi.fn()} />,
        );
        screen.getByRole("combobox").focus();

        await user.keyboard(key);
        expect(screen.getByRole("listbox"), `expected ${key} to open the panel`).toBeInTheDocument();

        unmount();
      }
    });

    it("seeds the active option from the current selection when opening", async () => {
      const user = userEvent.setup();
      const { trigger } = renderDropdown({ value: "k" });
      trigger.focus();

      await user.keyboard("{ArrowDown}");

      expect(screen.getByRole("option", { name: /Potassium/ })).toHaveClass("dropdown__option--active");
    });

    it("seeds the first enabled option when nothing is selected", async () => {
      const user = userEvent.setup();
      const { trigger } = renderDropdown();
      trigger.focus();

      await user.keyboard("{ArrowDown}");

      expect(screen.getByRole("option", { name: /White cell count/ })).toHaveClass(
        "dropdown__option--active",
      );
    });

    // A selected-but-disabled option would put the cursor somewhere Enter
    // can't commit, so seeding falls back to the first enabled option.
    it("falls back to the first enabled option when the selection is disabled", async () => {
      const user = userEvent.setup();
      const { trigger } = renderDropdown({ value: "na" });
      trigger.focus();

      await user.keyboard("{ArrowDown}");

      expect(screen.getByRole("option", { name: /White cell count/ })).toHaveClass(
        "dropdown__option--active",
      );
    });

    it("opens at the last enabled option on ArrowUp", async () => {
      const user = userEvent.setup();
      const { trigger } = renderDropdown();
      trigger.focus();

      await user.keyboard("{ArrowUp}");

      expect(screen.getByRole("option", { name: /Potassium/ })).toHaveClass("dropdown__option--active");
    });

    it("skips disabled options when navigating", async () => {
      const user = userEvent.setup();
      const { trigger } = renderDropdown({ value: "hb" });
      trigger.focus();

      await user.keyboard("{ArrowDown}{ArrowDown}");

      // Haemoglobin -> (skips Sodium) -> Potassium
      expect(screen.getByRole("option", { name: /Potassium/ })).toHaveClass("dropdown__option--active");
    });

    it("does not wrap past the last option", async () => {
      const user = userEvent.setup();
      const { trigger } = renderDropdown({ value: "k" });
      trigger.focus();

      await user.keyboard("{ArrowDown}{ArrowDown}{ArrowDown}");

      expect(screen.getByRole("option", { name: /Potassium/ })).toHaveClass("dropdown__option--active");
    });

    it("jumps to the first enabled option on Home", async () => {
      const user = userEvent.setup();
      const { trigger } = renderDropdown({ value: "k" });
      trigger.focus();

      await user.keyboard("{ArrowDown}{Home}");

      expect(screen.getByRole("option", { name: /White cell count/ })).toHaveClass(
        "dropdown__option--active",
      );
    });

    it("jumps to the last enabled option on End", async () => {
      const user = userEvent.setup();
      const { trigger } = renderDropdown();
      trigger.focus();

      await user.keyboard("{ArrowDown}{End}");

      expect(screen.getByRole("option", { name: /Potassium/ })).toHaveClass("dropdown__option--active");
    });

    it("selects the active option on Enter", async () => {
      const user = userEvent.setup();
      const { onChange, trigger } = renderDropdown();
      trigger.focus();

      await user.keyboard("{ArrowDown}{ArrowDown}{Enter}");

      expect(onChange).toHaveBeenCalledWith("hb");
      expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
    });

    // Space commits here; the filterable variant must type a space instead.
    it("selects the active option on Space", async () => {
      const user = userEvent.setup();
      const { onChange, trigger } = renderDropdown();
      trigger.focus();

      await user.keyboard("{ArrowDown}[Space]");

      expect(onChange).toHaveBeenCalledWith("wbc");
    });

    it("closes without selecting on Escape", async () => {
      const user = userEvent.setup();
      const { onChange, trigger } = renderDropdown();
      trigger.focus();

      await user.keyboard("{ArrowDown}{Escape}");

      expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
      expect(onChange).not.toHaveBeenCalled();
    });

    it("closes and moves focus on Tab", async () => {
      const user = userEvent.setup();
      render(
        <>
          <Dropdown label="Analyte" value={null} options={options} onChange={vi.fn()} />
          <button type="button">after</button>
        </>,
      );
      screen.getByRole("combobox").focus();

      await user.keyboard("{ArrowDown}");
      await user.tab();

      expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
      expect(screen.getByRole("button", { name: "after" })).toHaveFocus();
    });

    it("points aria-activedescendant at the active option", async () => {
      const user = userEvent.setup();
      const { trigger } = renderDropdown();
      trigger.focus();

      await user.keyboard("{ArrowDown}");

      const activeId = trigger.getAttribute("aria-activedescendant");
      expect(activeId).toBeTruthy();
      expect(screen.getByRole("option", { name: /White cell count/ })).toHaveAttribute("id", activeId!);
    });

    it("omits aria-activedescendant when no option is selectable", async () => {
      const user = userEvent.setup();
      const { trigger } = renderDropdown({ options: [] });

      await user.click(trigger);

      expect(trigger).not.toHaveAttribute("aria-activedescendant");
    });
  });

  describe("when options change while open", () => {
    it("keeps the cursor on the highlighted option when rows are inserted above it", async () => {
      const user = userEvent.setup();
      const onChange = vi.fn();
      const { rerender } = render(<Dropdown label="Analyte" value={null} options={options} onChange={onChange} />);

      await user.click(screen.getByRole("combobox"));
      await user.keyboard("{ArrowDown}");
      rerender(
        <Dropdown label="Analyte" value={null} options={[{ value: "cr", label: "Creatinine" }, ...options]} onChange={onChange} />,
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
      const { rerender } = render(<Dropdown label="Analyte" value={null} options={options} onChange={onChange} />);

      await user.click(screen.getByRole("combobox"));
      await user.keyboard("{ArrowDown}");
      rerender(
        <Dropdown label="Analyte" value={null} options={withoutHighlighted} onChange={onChange} />,
      );
      await user.keyboard("{Enter}");

      expect(onChange).not.toHaveBeenCalled();
    });
  });

  it("does not move the cursor onto a disabled option on hover", async () => {
    const user = userEvent.setup();
    const { onChange, trigger } = renderDropdown();

    await user.click(trigger);
    await user.hover(screen.getByRole("option", { name: "Sodium" }));
    await user.keyboard("{Enter}");

    expect(onChange).toHaveBeenCalledWith("wbc");
  });

  it("keeps the label as the accessible name when it is visually hidden", () => {
    renderDropdown({ labelHidden: true });

    expect(screen.getByRole("combobox", { name: "Analyte" })).toBeInTheDocument();
    expect(screen.getByText("Analyte")).toHaveClass("dropdown__label--hidden");
  });

  describe("required", () => {
    function renderInForm(props: Partial<React.ComponentProps<typeof Dropdown<string>>>) {
      const onSubmit = vi.fn((e: React.FormEvent) => e.preventDefault());
      render(
        <form onSubmit={onSubmit}>
          <Dropdown label="Analyte" value={null} options={options} onChange={vi.fn()} {...props} />
          <button type="submit">Save</button>
        </form>,
      );
      return { onSubmit };
    }

    it("blocks submit while nothing is selected", async () => {
      const user = userEvent.setup();
      const { onSubmit } = renderInForm({ required: true });

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

    // The browser focuses the first invalid control to report it; that
    // has to land on the trigger, not the invisible input.
    it("hands focus to the trigger when the validation input is focused", () => {
      const { container } = render(
        <Dropdown label="Analyte" value={null} options={options} onChange={vi.fn()} required />,
      );

      container.querySelector<HTMLInputElement>(".dropdown__validation")!.focus();

      expect(screen.getByRole("combobox")).toHaveFocus();
    });
  });

  describe("scrolling", () => {
    // jsdom has no scrollIntoView, so it's stubbed per test and the
    // element it was called on is read back from the mock's `this`.
    function stubScrollIntoView() {
      const scroll = vi.fn();
      Object.defineProperty(HTMLElement.prototype, "scrollIntoView", { value: scroll, configurable: true });
      return {
        scrolledTo: () => (scroll.mock.contexts.at(-1) as HTMLElement | undefined)?.textContent,
        calls: () => scroll.mock.calls.length,
        restore: () => delete (HTMLElement.prototype as Partial<HTMLElement>).scrollIntoView,
      };
    }

    it("scrolls the active option into view as the cursor moves", async () => {
      const stub = stubScrollIntoView();
      try {
        const user = userEvent.setup();
        const { trigger } = renderDropdown();

        await user.click(trigger);
        expect(stub.scrolledTo()).toContain("White cell count");

        await user.keyboard("{ArrowDown}");
        expect(stub.scrolledTo()).toContain("Haemoglobin");
      } finally {
        stub.restore();
      }
    });

    it("scrolls again on reopen even when the active option is unchanged", async () => {
      const stub = stubScrollIntoView();
      try {
        const user = userEvent.setup();
        const { trigger } = renderDropdown();

        await user.click(trigger);
        await user.keyboard("{Escape}");
        const before = stub.calls();
        await user.click(trigger);

        expect(stub.calls()).toBeGreaterThan(before);
      } finally {
        stub.restore();
      }
    });
  });
});
