import { lazyRoute } from "@/shared/lazy_route/lazy_route";
export { ACCOUNT_PATH } from "./account_path";
export const AccountRoute = lazyRoute(() => import("./account_route"));
