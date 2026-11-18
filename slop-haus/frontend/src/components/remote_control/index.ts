import { lazyRoute } from "@/shared/lazy_route/lazy_route";
export type { RemoteControlSelection } from "./remote_control_page";
export { parseRemoteControlView, type RemoteControlView } from "./remote_control_view";
export const RemoteControlRoute = lazyRoute(() => import("./remote_control_route"));
