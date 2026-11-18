import { lazyRoute } from "@/shared/lazy_route/lazy_route";
export { default as StewardNotices } from "./steward_notices";
export const StewardDigestPage = lazyRoute(() => import("./steward_digest_page"));
