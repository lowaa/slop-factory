import { lazyRoute } from "@/shared/lazy_route/lazy_route";
export { clearPendingInvite, hasPendingInviteToken } from "./pending_invite";
export const InviteRoute = lazyRoute(() => import("./invite_route"));
