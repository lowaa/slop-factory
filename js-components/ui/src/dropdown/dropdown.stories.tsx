import { useState } from "react";
import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, userEvent, within } from "storybook/test";
import Button from "../button/button";
import Dropdown, { type DropdownProps } from "./dropdown";
import type { DropdownOption } from "./option";

const analytes: DropdownOption[] = [
  { value: "wbc", label: "White cell count", hint: "haem" },
  { value: "hb", label: "Haemoglobin", hint: "haem" },
  { value: "plt", label: "Platelets", hint: "haem" },
  { value: "na", label: "Sodium", hint: "lytes" },
  { value: "k", label: "Potassium", hint: "lytes" },
  { value: "crp", label: "C-reactive protein", hint: "inflam", disabled: true },
];

// Alternating disabled rows, rather than one at the far end (as `analytes`
// has) -- shows the skip-over styling and behaviour repeatedly rather than
// just once, and gives the keyboard-navigation play function below more
// than a single disabled row to actually skip past.
const withManyDisabled: DropdownOption[] = [
  { value: "wbc", label: "White cell count", hint: "haem" },
  { value: "hb", label: "Haemoglobin", hint: "haem", disabled: true },
  { value: "plt", label: "Platelets", hint: "haem" },
  { value: "na", label: "Sodium", hint: "lytes", disabled: true },
  { value: "k", label: "Potassium", hint: "lytes" },
  { value: "cl", label: "Chloride", hint: "lytes", disabled: true },
  { value: "crp", label: "C-reactive protein", hint: "inflam" },
];

// Dropdown is controlled, so every story needs a state owner -- rendering
// it with a fixed `value` would show a picker that refuses to change.
type ControlledProps = Partial<Omit<DropdownProps<string>, "value" | "onChange">> & {
  initial?: string | null;
};

function Controlled({ initial = null, ...props }: ControlledProps) {
  const [value, setValue] = useState<string | null>(initial);
  return <Dropdown label="Analyte" options={analytes} {...props} value={value} onChange={setValue} />;
}

const meta: Meta<typeof Dropdown> = {
  title: "components/Dropdown",
  component: Dropdown,
  // The panel is absolutely positioned; without room below it the open
  // state is clipped by the story canvas.
  decorators: [
    (Story) => (
      <div style={{ maxWidth: 280, paddingBottom: 260 }}>
        <Story />
      </div>
    ),
  ],
};

export default meta;
type Story = StoryObj<typeof Dropdown>;

export const Default: Story = {
  render: () => <Controlled />,
  // Keyboard-only path a mouse-driven jsdom test can't stand in for:
  // focus the trigger, open with a real ArrowDown key event, move the
  // cursor with more of them, and commit with Enter -- then check the
  // control's own displayed label, not just the onChange call.
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const trigger = canvas.getByRole("combobox");

    trigger.focus();
    await userEvent.keyboard("{ArrowDown}");
    await expect(canvas.findByRole("listbox")).resolves.toBeInTheDocument();

    await userEvent.keyboard("{ArrowDown}{ArrowDown}");
    await userEvent.keyboard("{Enter}");

    expect(canvas.queryByRole("listbox")).not.toBeInTheDocument();
    expect(canvas.getByText("Platelets")).toBeInTheDocument();
    expect(trigger).toHaveFocus();
  },
};

export const WithSelection: Story = {
  render: () => <Controlled initial="hb" />,
};

export const Disabled: Story = {
  render: () => <Controlled initial="hb" disabled />,
};

export const Empty: Story = {
  render: () => (
    <Dropdown
      label="Analyte"
      value={null}
      options={[]}
      onChange={() => {}}
      emptyMessage="No analytes configured."
    />
  ),
};

// The label is still the control's accessible name even though it's
// clipped from layout -- exactly what a screen reader user relies on,
// which only a real accessibility-tree query (not a visual jsdom render)
// can confirm end to end.
export const LabelHidden: Story = {
  render: () => <Controlled labelHidden />,
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const trigger = canvas.getByRole("combobox", { name: "Analyte" });

    trigger.focus();
    await userEvent.keyboard("{ArrowDown}");
    expect(canvas.getByRole("listbox")).toBeInTheDocument();

    // Escape closes without committing, and -- unlike a mouse-driven
    // close -- keyboard focus has nowhere else to go, so it must stay on
    // the control that opened the panel.
    await userEvent.keyboard("{Escape}");
    expect(canvas.queryByRole("listbox")).not.toBeInTheDocument();
    expect(trigger).toHaveFocus();
  },
};

// Hints line up in their own column to the right of the label -- shown
// for every option here, rather than `analytes`' single haem/lytes/inflam
// split, so the alignment reads clearly against a longer label too.
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
    await userEvent.click(canvas.getByRole("combobox"));

    expect(canvas.getByRole("option", { name: /Bilirubin/ })).toHaveTextContent("µmol/L");
  },
};

// Several disabled rows among enabled ones (see `withManyDisabled` above),
// not just a single one at the end -- ArrowDown here has to keep skipping
// until it finds the next enabled row.
export const MixedDisabledOptions: Story = {
  render: function MixedDisabledOptionsStory() {
    const [value, setValue] = useState<string | null>(null);
    return <Dropdown label="Analyte" value={value} options={withManyDisabled} onChange={setValue} />;
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const trigger = canvas.getByRole("combobox");

    trigger.focus();
    await userEvent.keyboard("{ArrowDown}"); // seeds on White cell count (first enabled)
    await userEvent.keyboard("{ArrowDown}"); // Haemoglobin is disabled -> skips to Platelets
    await userEvent.keyboard("{Enter}");

    expect(canvas.getByText("Platelets")).toBeInTheDocument();
  },
};

// Blocks the surrounding form's submit while nothing is selected, the way
// a native <select required> does -- including the browser's own
// invalid-focus behaviour on submit, which jsdom doesn't implement at all.
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
        <Dropdown label="Analyte" value={value} options={analytes} onChange={setValue} required />
        <Button type="submit">Save</Button>
        {submitted && <p>submitted</p>}
      </form>
    );
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const trigger = canvas.getByRole("combobox");
    expect(trigger).toHaveAttribute("aria-required", "true");

    // A real click on the submit button, invalid form and all -- the
    // browser cancels the submit before the handler ever runs and, same
    // as a native <select required>, focuses the first invalid control.
    // Neither half of that is something jsdom implements.
    await userEvent.click(canvas.getByRole("button", { name: "Save" }));

    expect(canvas.queryByText("submitted")).not.toBeInTheDocument();
    // This dropdown's hidden validation input hands focus back to the
    // trigger (see validation_input.tsx) on that same browser-driven event.
    expect(trigger).toHaveFocus();
  },
};

// Exercises the scroll-into-view behaviour and the panel's max-height.
export const LongList: Story = {
  render: function LongListStory() {
    const [value, setValue] = useState<string | null>(null);
    const many = Array.from({ length: 40 }, (_, i) => ({
      value: `analyte-${i}`,
      label: `Analyte ${i + 1}`,
      hint: i % 3 === 0 ? "haem" : "chem",
    }));
    return <Dropdown label="Analyte" value={value} options={many} onChange={setValue} />;
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const trigger = canvas.getByRole("combobox");

    trigger.focus();
    await userEvent.keyboard("{ArrowDown}");
    // Past the panel's own max-height (260px) worth of rows -- jsdom has
    // no layout at all, so whether this actually scrolled into view can
    // only be checked against real bounding boxes in a real browser.
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
