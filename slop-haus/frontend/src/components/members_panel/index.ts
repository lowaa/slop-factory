import { lazyRoute } from "@/shared/lazy_route/lazy_route";
export const MembersPanel = lazyRoute(() => import("./members_panel"));
