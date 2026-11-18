import { lazyRoute } from "@/shared/lazy_route/lazy_route";
export { editWarning } from "./attempt_edit_warning";
export { default as AttemptEditWarning } from "./attempt_edit_warning_note";
export type { AttemptLink } from "./attempt_view";
export { default as DiscussShortcut } from "./discuss_shortcut";
const loadTicketRoutes = () => import("./ticket_routes");
export const TicketRoute = lazyRoute(() => loadTicketRoutes().then((m) => ({ default: m.TicketRoute })));
export const TicketAttemptRoute = lazyRoute(() => loadTicketRoutes().then((m) => ({ default: m.TicketAttemptRoute })));
// For tests: lets them pay the route's cold import before the interaction they time.
export const preloadTicketRoutes = async () => {
  await loadTicketRoutes();
};
export { BodyDiff } from "./ticket_history";
export { type TicketView, TICKET_VIEWS } from "./ticket_view";
