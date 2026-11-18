import { useState } from "react";
import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, userEvent, within } from "storybook/test";
import ModelessDialog from "./modeless_dialog";
import DialogDock from "./dialog_dock";

const meta: Meta<typeof ModelessDialog> = {
  title: "components/ModelessDialog",
  component: ModelessDialog,
  args: { title: "Wed 15 Jan 2026, 9:30 am", onClose: () => {} },
  decorators: [
    (Story) => (
      <div style={{ maxWidth: 320, padding: 8 }}>
        <Story />
      </div>
    ),
  ],
};

export default meta;
type Story = StoryObj<typeof ModelessDialog>;

export const Default: Story = {
  args: { children: <p style={{ margin: 0 }}>Sodium 137 mmol/L</p> },
};

export const WithPip: Story = {
  args: { pipColor: "#0072b2", children: <p style={{ margin: 0 }}>Sodium 137 mmol/L</p> },
};

// A long title wraps rather than pushing the close button out of the card.
export const LongTitle: Story = {
  args: {
    pipColor: "#cc79a7",
    title: "Thursday 15 January 2026, 9:30 am -- Royal Hospital pathology, second draw of the day",
  },
};

const seed = [
  { id: 1, title: "15 Jan 2026", color: "#0072b2", body: "Sodium 137 mmol/L" },
  { id: 2, title: "1 Feb 2026", color: "#cc79a7", body: "Sodium 141 mmol/L" },
  { id: 3, title: "3 Mar 2026", color: "#009e73", body: "Sodium 139 mmol/L" },
];

// The dock owns no state of its own -- the host decides what is open -- so
// the story plays host.
function Docked() {
  const [open, setOpen] = useState(seed);
  return (
    <div style={{ display: "grid", gridTemplateColumns: "1fr 260px", gap: 16 }}>
      <p style={{ margin: 0 }}>The page's content stays visible and usable beside the dock.</p>
      <DialogDock label="Open invoices">
        {open.map((d) => (
          <ModelessDialog
            key={d.id}
            title={d.title}
            headingLevel={3}
            pipColor={d.color}
            closeLabel={`Close ${d.title}`}
            onClose={() => setOpen((all) => all.filter((o) => o.id !== d.id))}
          >
            <p style={{ margin: 0 }}>{d.body}</p>
          </ModelessDialog>
        ))}
      </DialogDock>
    </div>
  );
}

export const Dock: Story = {
  decorators: [
    (Story) => (
      <div style={{ maxWidth: 640 }}>
        <Story />
      </div>
    ),
  ],
  render: () => <Docked />,
  // Real focus and key events in a browser: Escape closes the dialog the
  // user is in and leaves the rest; the close button closes its own.
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const dock = canvas.getByRole("complementary", { name: "Open invoices" });
    expect(within(dock).getAllByRole("dialog")).toHaveLength(3);

    await userEvent.click(canvas.getByText("Sodium 141 mmol/L"));
    expect(canvas.getByRole("dialog", { name: "1 Feb 2026" })).toHaveFocus();
    await userEvent.keyboard("{Escape}");

    expect(canvas.queryByRole("dialog", { name: "1 Feb 2026" })).not.toBeInTheDocument();
    expect(canvas.getAllByRole("dialog").map((d) => d.getAttribute("aria-modal"))).toEqual(["false", "false"]);

    await userEvent.click(canvas.getByRole("button", { name: "Close 15 Jan 2026" }));
    expect(canvas.getAllByRole("dialog")).toHaveLength(1);
    expect(canvas.getByRole("dialog", { name: "3 Mar 2026" })).toBeVisible();
  },
};
