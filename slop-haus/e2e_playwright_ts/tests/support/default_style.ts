import { DefaultStyleChecker } from "@e2e-support/default_style";
import { firstExistingPath } from "@e2e-support/first_existing_path";

// The image bakes the default-look check in beside the tests (see its
// Dockerfile); a run from the checkout reads it where it lives.
export const defaultStyleChecker = new DefaultStyleChecker(
  firstExistingPath("the storybook-kit default_style_check.ts", [
    process.env.DEFAULT_STYLE_CHECK_PATH,
    "/srv/default_style_check.ts",
    new URL("../../../../js-components/storybook-kit/src/default_style_check.ts", import.meta.url).pathname,
  ]),
);
