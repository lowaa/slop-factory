import { dirname } from "node:path";
import { fileURLToPath } from "node:url";
import { checkStoryCoverage, type CoveredByParent } from "@slop-factory/storybook-kit/story_coverage";

// Components with no story of their own because another story renders their
// controls, keyed by path relative to src/. Each entry must name the story
// that covers it and why that is enough.
const COVERED_BY_PARENT_STORY: Record<string, CoveredByParent> = {
  "dropdown/validation_input.tsx": {
    story: "dropdown/dropdown.stories.tsx",
    reason:
      "A visually hidden validation input, rendered only inside the Dropdown and FilterableDropdown it serves; their Required stories render it.",
  },
};

checkStoryCoverage({
  srcDir: dirname(fileURLToPath(import.meta.url)),
  coveredByParent: COVERED_BY_PARENT_STORY,
  mustFind: 4,
});
