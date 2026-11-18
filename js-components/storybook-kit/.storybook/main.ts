import type { StorybookConfig } from "@storybook/react-vite";

// The kit's stories exist to test the default-look check in a real browser,
// so this Storybook carries no app providers or global stylesheet.
const config: StorybookConfig = {
  stories: ["../src/**/*.stories.@(ts|tsx)"],
  addons: ["@storybook/addon-a11y", "@storybook/addon-vitest"],
  core: { disableTelemetry: true },
  framework: { name: "@storybook/react-vite", options: {} },
};

export default config;
