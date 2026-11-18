import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { checkStoryCoverage, type CoveredByParent } from "@slop-factory/storybook-kit/story_coverage";

// Components with no story of their own because another story renders their
// controls, keyed by path relative to src/. Each entry must name the story
// that covers it and why that is enough (typically: it renders the
// component inside the parent's states and the stylesheet is shared).
const TENANT_SETTINGS_STORY = "components/tenant_settings_page/tenant_settings_page.stories.tsx";
const TENANT_SETTINGS_REASON =
  "only ever rendered inside the page, whose stories play each section, the history and a failed write";
const COVERED_BY_PARENT_STORY: Record<string, CoveredByParent> = {
  "components/tenant_settings_page/setting_history.tsx": { story: TENANT_SETTINGS_STORY, reason: TENANT_SETTINGS_REASON },
  "components/tenant_settings_page/your_settings_section.tsx": { story: TENANT_SETTINGS_STORY, reason: TENANT_SETTINGS_REASON },
  "components/tenant_settings_page/setting_switch_row.tsx": { story: TENANT_SETTINGS_STORY, reason: TENANT_SETTINGS_REASON },
  "components/tenant_settings_page/this_tenant_section.tsx": { story: TENANT_SETTINGS_STORY, reason: TENANT_SETTINGS_REASON },
  "components/agents_panel/agent_role_editor/agent_role_editor.tsx": {
    story: "components/agents_panel/agent_row.stories.tsx",
    reason: "only ever rendered inside the row, whose EditRolesOpen story opens it",
  },
  "components/ticket_detail/ticket_actions.tsx": {
    story: "components/ticket_detail/ticket_detail.stories.tsx",
    reason: "only ever rendered inside the page, whose stories show the Preparing, parked and timer states",
  },
  "components/ticket_detail/ticket_meta.tsx": {
    story: "components/ticket_detail/ticket_detail.stories.tsx",
    reason: "only ever rendered inside the page, which every story of it shows",
  },
};

checkStoryCoverage({
  srcDir: join(dirname(fileURLToPath(import.meta.url)), ".."),
  coveredByParent: COVERED_BY_PARENT_STORY,
  mustFind: 21,
});
