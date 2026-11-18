import { dirname } from "node:path";
import { fileURLToPath } from "node:url";
import { checkStoryCoverage, type CoveredByParent } from "@slop-factory/storybook-kit/story_coverage";

// Components with no story of their own because another story renders their
// controls, keyed by path relative to src/. Each entry must name the story
// that covers it and why that is enough.
const COVERED_BY_PARENT_STORY: Record<string, CoveredByParent> = {
  "consent/cookie_preferences_button.tsx": {
    story: "consent/consent.stories.tsx",
    reason: "The stories render it as the footer entry and click it (WithdrawingFromTheFooter).",
  },
  "consent/do_not_sell_button.tsx": {
    story: "consent/consent.stories.tsx",
    reason: "Rendered in the stories' footer next to the cookie preferences button, and shares its stylesheet.",
  },
  "consent/preferences_dialog.tsx": {
    story: "consent/consent.stories.tsx",
    reason: "Opened from the banner and from the footer button by PreferencesFromTheBanner and WithdrawingFromTheFooter.",
  },
};

checkStoryCoverage({
  srcDir: dirname(fileURLToPath(import.meta.url)),
  coveredByParent: COVERED_BY_PARENT_STORY,
  mustFind: 3,
});
