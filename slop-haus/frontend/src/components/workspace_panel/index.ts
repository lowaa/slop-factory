import { lazyRoute } from "@/shared/lazy_route/lazy_route";
export const WorkspacePanel = lazyRoute(() => import("./workspace_panel"));
