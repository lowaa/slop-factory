import type { StorybookConfig } from "@storybook/react-vite";

// Colocated with this package's own components -- repo-wide shared
// UI lives here (not in any one app's src tree), so unlike an
// app's own Storybook this one must stay free of any
// app-specific decorators (no AuthProvider/StaticConfigProvider) --
// components documented here are meant to be portable across consumers.
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
