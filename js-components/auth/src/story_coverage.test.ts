import { dirname } from "node:path";
import { fileURLToPath } from "node:url";
import { checkStoryCoverage, type CoveredByParent } from "@slop-factory/storybook-kit/story_coverage";

// Components with no story of their own because another story renders their
// controls, keyed by path relative to src/. Each entry must name the story
// that covers it and why that is enough.
const COVERED_BY_PARENT_STORY: Record<string, CoveredByParent> = {
  "sign_in_methods/step_up_prompt.tsx": {
    story: "sign_in_methods/sign_in_methods_panel.stories.tsx",
    reason: "Only the panel renders it; StepUpWithPassword, StepUpWithProvider and WrongPasswordAtStepUp show it.",
  },
  "test/sign_up_extras_fixture.tsx": {
    story: "register_page/register_page.stories.tsx",
    reason:
      "A fixture for the component tests, never rendered in a story or shipped; a real app's sign-up extras are checked by that app's own stories.",
  },
};

checkStoryCoverage({
  srcDir: dirname(fileURLToPath(import.meta.url)),
  coveredByParent: COVERED_BY_PARENT_STORY,
  mustFind: 8,
});
