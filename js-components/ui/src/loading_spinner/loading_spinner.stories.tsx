import type { Meta, StoryObj } from "@storybook/react-vite";
import LoadingSpinner from "./loading_spinner";

const meta: Meta<typeof LoadingSpinner> = {
  title: "components/LoadingSpinner",
  component: LoadingSpinner,
};

export default meta;
type Story = StoryObj<typeof LoadingSpinner>;

export const Default: Story = {};
