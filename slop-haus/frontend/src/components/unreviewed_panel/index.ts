import { lazyRoute } from "@/shared/lazy_route/lazy_route";
export const UnreviewedRoute = lazyRoute(() => import("./unreviewed_route"));
