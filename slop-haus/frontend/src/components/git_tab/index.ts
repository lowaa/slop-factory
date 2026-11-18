import { lazyRoute } from "@/shared/lazy_route/lazy_route";
export const GitTab = lazyRoute(() => import("./git_tab"));
