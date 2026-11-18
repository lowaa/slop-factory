import { lazyRoute } from "@/shared/lazy_route/lazy_route";
export const TenantSettingsPage = lazyRoute(() => import("./tenant_settings_page"));
