import { useState } from "react";
import type { Meta, StoryObj } from "@storybook/react-vite";
import Tabs from "./tabs";

const items = [
  { id: "one", label: "First" },
  { id: "two", label: "Second" },
];

function Demo() {
  const [value, setValue] = useState("one");
  return (
    <Tabs items={items} value={value} onChange={setValue} label="Example">
      <p>Showing {value}.</p>
    </Tabs>
  );
}

const meta = {
  title: "Widgets/Tabs",
  component: Demo,
  parameters: { layout: "padded" },
} satisfies Meta<typeof Demo>;

export default meta;
type Story = StoryObj<typeof meta>;

export const Default: Story = {};
