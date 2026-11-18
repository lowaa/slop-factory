// Shared by slop-haus and js-components; each runs it through
// dev-tools/style-check/stylelint_ratchet from its own test.sh, against its
// own stylelint-baseline.json. See dev-tools/style-check/stylelint_ratchet/README.md.

const COLOUR_FUNCTIONS = ["rgb", "rgba", "hsl", "hsla", "hwb", "lab", "lch", "oklab", "oklch"];
const SIZE_MEDIA_FEATURES = [
  "width",
  "min-width",
  "max-width",
  "height",
  "min-height",
  "max-height",
];

// The token definitions: the only places a colour may be spelled out.
const COLOUR_TOKEN_FILES = [
  "**/src/index.scss",
  "js-components/*/.storybook/*.scss",
];

// Where a breakpoint is defined once, as a mixin the rest of the app uses.
const BREAKPOINT_FILES = ["**/src/styles/_layout.scss"];

export default {
  extends: ["stylelint-config-standard-scss"],
  // A disable comment is the sanctioned exception, so it has to say why.
  reportDescriptionlessDisables: true,
  ignoreFiles: ["**/node_modules/**", "**/dist/**", "**/storybook-static/**"],
  rules: {
    "color-no-hex": true,
    "function-disallowed-list": COLOUR_FUNCTIONS,
    "media-feature-name-disallowed-list": SIZE_MEDIA_FEATURES,
    "declaration-no-important": true,
    // Anything that is not a token is a hand-picked size or layer.
    "declaration-property-value-allowed-list": {
      "font-size": ["/^var\\(--text-[a-z0-9-]+\\)$/", "inherit"],
      "z-index": ["/^var\\(--z-[a-z0-9-]+\\)$/", "auto", "0", "-1"],
    },
  },
  overrides: [
    {
      files: COLOUR_TOKEN_FILES,
      rules: {
        "color-no-hex": null,
        "function-disallowed-list": null,
      },
    },
    {
      files: BREAKPOINT_FILES,
      rules: { "media-feature-name-disallowed-list": null },
    },
    {
      // A fallback in a shared component hides a token that was never
      // defined, so the consumer sees a plausible but wrong value.
      files: ["js-components/**/*.scss"],
      rules: {
        "declaration-property-value-disallowed-list": {
          "/.*/": ["/var\\([^()]*,/"],
        },
      },
    },
  ],
};
