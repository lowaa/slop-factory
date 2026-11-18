import { lazyRoute } from "@/shared/lazy_route/lazy_route";
export { BLOCKED_FILTER_MODEL, parseBlockedSearch } from "./blocked_filter";
export const BlockedRoute = lazyRoute(() => import("./blocked_route"));
