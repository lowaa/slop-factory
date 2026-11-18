import { lazyRoute } from "@/shared/lazy_route/lazy_route";
export const GitHubAppCreatedRoute = lazyRoute(() => import("./github_app_routes").then((m) => ({ default: m.GitHubAppCreatedRoute })));
export const GitHubAppInstalledRoute = lazyRoute(() => import("./github_app_routes").then((m) => ({ default: m.GitHubAppInstalledRoute })));
