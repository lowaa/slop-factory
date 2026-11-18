import { lazyRoute } from "@/shared/lazy_route/lazy_route";
export { HELP_PATH, supervisorGuidePath } from "./guide_pages";
export const HelpIndexRoute = lazyRoute(() => import("./help_routes").then((m) => ({ default: m.HelpIndex })));
export const HelpPageRoute = lazyRoute(() => import("./help_routes").then((m) => ({ default: m.HelpPageRoute })));
