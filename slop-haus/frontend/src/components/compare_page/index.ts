import { lazyRoute } from "@/shared/lazy_route/lazy_route";

export const ComparePage = lazyRoute(() => import("./compare_page"));
