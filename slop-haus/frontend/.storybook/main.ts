import type { StorybookConfig } from "@storybook/react-vite";

// Storybook reuses vite.config.ts as-is (the "@/" alias, the react plugin,
// sass), so there is no second build config to drift from the app's.
const config: StorybookConfig = {
  stories: ["../src/**/*.stories.@(ts|tsx)"],
  addons: ["@storybook/addon-a11y", "@storybook/addon-vitest"],
  // Storybook phones home with anonymous usage telemetry by default. This
  // is a local tool for one person's cluster; nothing about how it is
  // developed needs to leave the machine, and a build step that makes a
  // network call nobody asked for is a surprise, not a feature.
  core: {
    disableTelemetry: true,
  },
  framework: {
    name: "@storybook/react-vite",
    options: {},
  },
};

export default config;
