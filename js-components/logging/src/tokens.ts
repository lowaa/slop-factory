// The design tokens this package's styles read, which the app must define.
// tokens.test.ts fails when the styles read one that is not listed here.
export const REQUIRED_TOKENS: readonly string[] = [
  "--color-bg-raised",
  "--color-border",
  "--color-text-muted",
  "--radius-md",
  "--space-2",
  "--space-3",
  "--space-4",
  "--text-md",
  "--z-toast",
];

export const INTERNAL_TOKENS: readonly string[] = [];
