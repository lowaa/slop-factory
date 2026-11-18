import type { StorybookConfig } from "@storybook/react-vite";

// Colocated with this package's own components -- repo-wide shared
// auth pages live here (not in any one app's src tree), so like
// js-components/ui's Storybook this one stays
// free of any app-specific decorator beyond AuthPagesProvider itself, and
// wraps it with a fake AuthApi rather than a real app's config (see
// preview.tsx).
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
