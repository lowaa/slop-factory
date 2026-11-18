import { lazyRoute } from "@/shared/lazy_route/lazy_route";
export const ApprovalsRoute = lazyRoute(() => import("./approvals_route"));
export { default as ProposalCard } from "./proposal_card";
