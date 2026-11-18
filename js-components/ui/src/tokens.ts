// The design tokens this package's styles read, which the app must define.
// tokens.test.ts fails when the styles read one that is not listed here.
export const REQUIRED_TOKENS: readonly string[] = [
  "--color-accent",
  "--color-accent-dim",
  "--color-accent-hover",
  "--color-accent-on",
  "--color-accent-text",
  "--color-bg-raised",
  "--color-bg-sunken",
  "--color-border",
  "--color-border-strong",
  "--color-danger",
  "--color-danger-bg",
  "--color-text",
  "--color-text-muted",
  "--color-text-subtle",
  "--glow-accent",
  "--glow-soft",
  "--radius",
  "--radius-sm",
  "--shadow-lg",
  "--shadow-sm",
  "--space-1",
  "--space-2",
  "--space-3",
  "--space-4",
  "--text-md",
  "--text-sm",
  "--text-xs",
  "--z-dropdown",
  "--z-menu",
];

// Set by the package itself (tooltip.tsx overrides it inline), so not asked
// of the app.
export const INTERNAL_TOKENS: readonly string[] = [
  "--tooltip-shift",
];
