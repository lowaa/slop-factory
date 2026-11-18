import { lazyRoute } from "@/shared/lazy_route/lazy_route";
export { NEW_TENANT_PATH } from "./new_tenant_path";
export const NewTenantPage = lazyRoute(() => import("./new_tenant_page"));
