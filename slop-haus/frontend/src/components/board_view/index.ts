import { lazyRoute } from "@/shared/lazy_route/lazy_route";
export const BoardRoute = lazyRoute(() => import("./board_route"));
