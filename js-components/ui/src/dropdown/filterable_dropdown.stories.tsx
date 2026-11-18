import { useState } from "react";
import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, userEvent, within } from "storybook/test";
import Button from "../button/button";
import FilterableDropdown, { type FilterableDropdownProps } from "./filterable_dropdown";
import type { DropdownOption } from "./option";

const analytes: DropdownOption[] = [
  { value: "wbc", label: "White cell count", hint: "haem" },
  { value: "hb", label: "Haemoglobin", hint: "haem" },
  { value: "plt", label: "Platelets", hint: "haem" },
  { value: "na", label: "Sodium", hint: "lytes" },
  { value: "k", label: "Potassium", hint: "lytes" },
  { value: "cl", label: "Chloride", hint: "lytes" },
  { value: "alt", label: "Alanine aminotransferase", hint: "liver" },
  { value: "alp", label: "Alkaline phosphatase", hint: "liver" },
  { value: "crp", label: "C-reactive protein", hint: "inflam", disabled: true },
];

// Alternating disabled rows -- see dropdown.stories.tsx's identical
// reasoning.
const withManyDisabled: DropdownOption[] = [
  { value: "wbc", label: "White cell count", hint: "haem" },
  { value: "hb", label: "Haemoglobin", hint: "haem", disabled: true },
  { value: "plt", label: "Platelets", hint: "haem" },
  { value: "na", label: "Sodium", hint: "lytes", disabled: true },
  { value: "k", label: "Potassium", hint: "lytes" },
  { value: "cl", label: "Chloride", hint: "lytes", disabled: true },
  { value: "crp", label: "C-reactive protein", hint: "inflam" },
];

type ControlledProps = Partial<Omit<FilterableDropdownProps<string>, "value" | "onChange">> & {
  initial?: string | null;
};

function Controlled({ initial = null, ...props }: ControlledProps) {
  const [value, setValue] = useState<string | null>(initial);
  return (
    <FilterableDropdown label="Analyte" options={analytes} {...props} value={value} onChange={setValue} />
  );
}

const meta: Meta<typeof FilterableDropdown> = {
  title: "components/FilterableDropdown",
  component: FilterableDropdown,
  decorators: [
    (Story) => (
      <div style={{ maxWidth: 280, paddingBottom: 300 }}>
        <Story />
      </div>
    ),
  ],
};

export default meta;
type Story = StoryObj<typeof FilterableDropdown>;

export const Default: Story = {
  render: () => <Controlled />,
  // Real typing, not a fixed filter value set via args -- this is the
  // behaviour jsdom can't stand in for: the input narrows the listbox as
  // each character lands, and a real Enter keypress commits the result.
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const input = canvas.getByRole("combobox");

    await userEvent.click(input);
    await userEvent.keyboard("aemo");

    const options = canvas.getAllByRole("option");
    expect(options).toHaveLength(1);
    expect(options[0]).toHaveTextContent("Haemoglobin");

    await userEvent.keyboard("{ArrowDown}{Enter}");

    expect(canvas.queryByRole("listbox")).not.toBeInTheDocument();
    expect(input).toHaveValue("Haemoglobin");
  },
};

// The selected label shows as placeholder once open, so the current choice
// stays readable while the box is cleared for typing.
export const WithSelection: Story = {
  render: () => <Controlled initial="hb" />,
};

export const Disabled: Story = {
  render: () => <Controlled initial="hb" disabled />,
};

export const Empty: Story = {
  render: () => (
    <FilterableDropdown
      label="Analyte"
      value={null}
      options={[]}
      onChange={() => {}}
      emptyMessage="No analytes configured."
    />
  ),
};

// See Dropdown's identical LabelHidden story -- the accessible name has to
// survive being visually hidden, and Escape has to leave keyboard focus on
// the input rather than dropping it, neither of which a jsdom render can
// confirm.
export const LabelHidden: Story = {
  render: () => <Controlled labelHidden />,
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const input = canvas.getByRole("combobox", { name: "Analyte" });

    await userEvent.click(input);
    expect(canvas.getByRole("listbox")).toBeInTheDocument();

    await userEvent.keyboard("{Escape}");
    expect(canvas.queryByRole("listbox")).not.toBeInTheDocument();
    expect(input).toHaveFocus();
  },
};

// Hints are searched as well as displayed -- filtering by a hint ("U/L")
// narrows to only the options that carry it.
export const WithHints: Story = {
  render: () => (
    <Controlled
      options={[
        { value: "alt", label: "Alanine aminotransferase", hint: "U/L" },
        { value: "alp", label: "Alkaline phosphatase", hint: "U/L" },
        { value: "bili", label: "Bilirubin", hint: "µmol/L" },
        { value: "alb", label: "Albumin", hint: "g/L" },
      ]}
    />
  ),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const input = canvas.getByRole("combobox");

    await userEvent.click(input);
    await userEvent.keyboard("µmol");

    const options = canvas.getAllByRole("option");
    expect(options).toHaveLength(1);
    expect(options[0]).toHaveTextContent("Bilirubin");
  },
};

// Several disabled rows among enabled ones -- ArrowDown has to keep
// skipping through the *filtered* list until it lands on an enabled one.
export const MixedDisabledOptions: Story = {
  render: function MixedDisabledOptionsStory() {
    const [value, setValue] = useState<string | null>(null);
    return <FilterableDropdown label="Analyte" value={value} options={withManyDisabled} onChange={setValue} />;
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const input = canvas.getByRole("combobox");

    // Opening (by click, here) already seeds the cursor on the first
    // enabled option -- White cell count -- unlike Dropdown's keyboard
    // open, which needs its own ArrowDown to do the same seeding.
    await userEvent.click(input);
    await userEvent.keyboard("{ArrowDown}"); // Haemoglobin is disabled -> skips to Platelets
    await userEvent.keyboard("{Enter}");

    expect(input).toHaveValue("Platelets");
  },
};

// See Dropdown's identical Required story -- the browser's own
// invalid-focus behaviour on submit isn't something jsdom implements.
export const Required: Story = {
  render: function RequiredStory() {
    const [value, setValue] = useState<string | null>(null);
    const [submitted, setSubmitted] = useState(false);
    return (
      <form
        onSubmit={(e) => {
          e.preventDefault();
          setSubmitted(true);
        }}
      >
        <FilterableDropdown label="Analyte" value={value} options={analytes} onChange={setValue} required />
        <Button type="submit">Save</Button>
        {submitted && <p>submitted</p>}
      </form>
    );
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const input = canvas.getByRole("combobox");
    expect(input).toHaveAttribute("aria-required", "true");

    // A real click on the submit button -- the browser cancels the
    // invalid submit before the handler runs and focuses the first
    // invalid control, neither of which jsdom implements.
    await userEvent.click(canvas.getByRole("button", { name: "Save" }));

    expect(canvas.queryByText("submitted")).not.toBeInTheDocument();
    expect(input).toHaveFocus();
  },
};

// Filtering matches the hint too, so typing a panel name ("liver")
// narrows to that group.
export const LongList: Story = {
  render: function LongListStory() {
    const [value, setValue] = useState<string | null>(null);
    const panels = ["haem", "lytes", "liver", "renal", "lipids"];
    const many = Array.from({ length: 60 }, (_, i) => ({
      value: `analyte-${i}`,
      label: `Analyte ${i + 1}`,
      hint: panels[i % panels.length],
    }));
    return <FilterableDropdown label="Analyte" value={value} options={many} onChange={setValue} />;
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const input = canvas.getByRole("combobox");

    await userEvent.click(input);
    for (let i = 0; i < 25; i++) {
      await userEvent.keyboard("{ArrowDown}");
    }

    const panel = canvas.getByRole("listbox");
    const active = panel.querySelector(".dropdown__option--active");
    if (!active) throw new Error("expected an active option after navigating");

    const panelRect = panel.getBoundingClientRect();
    const activeRect = active.getBoundingClientRect();
    expect(activeRect.top).toBeGreaterThanOrEqual(panelRect.top - 1);
    expect(activeRect.bottom).toBeLessThanOrEqual(panelRect.bottom + 1);
  },
};

// Pinned to the bottom of the viewport, where a menu that only opens
// downwards lands off-screen. Needs a real browser: jsdom has no layout.
export const PinnedToBottom: Story = {
  render: () => (
    <div style={{ position: "fixed", bottom: 16, left: 16, width: 280 }}>
      <Controlled />
    </div>
  ),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const trigger = canvas.getByRole("combobox");
    await userEvent.click(trigger);
    const panel = await canvas.findByRole("listbox");

    const panelRect = panel.getBoundingClientRect();
    expect(panelRect.top).toBeGreaterThanOrEqual(0);
    expect(panelRect.bottom).toBeLessThanOrEqual(trigger.getBoundingClientRect().top);
    expect(panel.querySelectorAll("[role=option]")).toHaveLength(analytes.length);
  },
};

// Where auto would open downwards, an explicit "top" must still win.
export const ExplicitTop: Story = {
  render: () => (
    <div style={{ paddingTop: 320 }}>
      <Controlled placement="top" />
    </div>
  ),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const trigger = canvas.getByRole("combobox");
    await userEvent.click(trigger);
    const panel = await canvas.findByRole("listbox");
    expect(panel.getBoundingClientRect().bottom).toBeLessThanOrEqual(trigger.getBoundingClientRect().top);
  },
};

// More rows than fit above the trigger: the menu is capped to the room
// and scrolls inside itself rather than running off the top of the screen.
export const PinnedToBottomLongList: Story = {
  render: () => (
    <div style={{ position: "fixed", bottom: 16, left: 16, width: 280 }}>
      <Controlled
        options={Array.from({ length: 60 }, (_, i) => ({ value: `analyte-${i}`, label: `Analyte ${i + 1}` }))}
      />
    </div>
  ),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const trigger = canvas.getByRole("combobox");
    await userEvent.click(trigger);
    const panel = await canvas.findByRole("listbox");

    const rect = panel.getBoundingClientRect();
    expect(rect.top).toBeGreaterThanOrEqual(0);
    expect(rect.bottom).toBeLessThanOrEqual(trigger.getBoundingClientRect().top);
    expect(panel.scrollHeight).toBeGreaterThan(panel.clientHeight);
  },
};
