// The design tokens this package's styles read, which the app must define.
// tokens.test.ts fails when the styles read one that is not listed here.
export const REQUIRED_TOKENS: readonly string[] = [
  "--color-accent",
  "--color-accent-hot",
  "--color-bg-raised",
  "--color-bg-sunken",
  "--color-border",
  "--color-border-strong",
  "--color-danger",
  "--color-link",
  "--color-text",
  "--color-text-muted",
  "--color-warning-bg",
  "--color-warning-border",
  "--color-warning-text",
  "--radius-md",
  "--radius-sm",
  "--shadow-lg",
  "--space-1",
  "--space-2",
  "--space-3",
  "--space-4",
  "--space-5",
  "--space-6",
  "--text-lg",
  "--text-md",
  "--text-sm",
  "--text-xl",
  "--z-modal",
  "--z-toast",
];

export const INTERNAL_TOKENS: readonly string[] = [];
