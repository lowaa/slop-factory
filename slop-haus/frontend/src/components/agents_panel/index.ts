import { lazyRoute } from "@/shared/lazy_route/lazy_route";
export const AgentsPanel = lazyRoute(() => import("./agents_panel"));
export { saveFile } from "./save_file";
export { describeStewardReport } from "./steward_report";
export { default as SupervisorOfflineBanner } from "./supervisor_offline_banner";
export { default as SupervisorSlots } from "./supervisor_slots";
