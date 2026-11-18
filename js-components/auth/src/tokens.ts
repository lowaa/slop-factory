// The design tokens this package's styles read, which the app must define.
// tokens.test.ts fails when the styles read one that is not listed here.
export const REQUIRED_TOKENS: readonly string[] = [
  "--color-accent-text",
  "--color-bg-raised",
  "--color-bg-sunken",
  "--color-border",
  "--color-border-strong",
  "--color-danger",
  "--color-danger-bg",
  "--color-text",
  "--color-text-muted",
  "--radius-lg",
  "--radius-sm",
  "--shadow-md",
  "--space-1",
  "--space-2",
  "--space-3",
  "--space-4",
  "--space-5",
  "--space-6",
  "--text-lg-plus",
  "--text-md",
  "--text-sm",
  "--text-xl",
];

export const INTERNAL_TOKENS: readonly string[] = [];
