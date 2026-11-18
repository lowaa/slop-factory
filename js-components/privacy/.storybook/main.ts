import type { StorybookConfig } from "@storybook/react-vite";

// Stories render the real components against a demo "server" (see
// src/consent/test/echo_api.ts), so a11y checks run on what the consent UI
// actually draws.
const config: StorybookConfig = {
  stories: ["../src/**/*.stories.@(ts|tsx)"],
  addons: ["@storybook/addon-a11y", "@storybook/addon-vitest"],
  core: { disableTelemetry: true },
  framework: {
    name: "@storybook/react-vite",
    options: {},
  },
};

export default config;
