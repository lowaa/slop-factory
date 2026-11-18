// A server change that alters a schema fails here, not in a UI test that
// happened to render the stale fixture: every factory's default output is
// checked against the schema it claims to be.
import { describe, expect, it } from "vitest";
import * as fixtures from "./fixtures";
import { openApiSchemas } from "./openapi_schema";

// The schema each factory builds. A factory with no row fails the
// completeness test below, so a new one cannot dodge the check.
const FACTORIES: Record<string, { schema: string; make: () => unknown }> = {
  makePath: { schema: "PathBody", make: () => fixtures.makePath() },
  makeTicket: { schema: "TicketBody", make: () => fixtures.makeTicket() },
  makeAttemptCard: { schema: "TicketBody", make: () => fixtures.makeAttemptCard() },
  makeReviewRun: { schema: "ReviewRunBody", make: () => fixtures.makeReviewRun({ findings: [fixtures.makeReviewFinding()] }) },
  makeReviewFinding: { schema: "ReviewFindingBody", make: () => fixtures.makeReviewFinding() },
  makeRevision: { schema: "RevisionBody", make: () => fixtures.makeRevision() },
  makeStatusChange: { schema: "StatusChangeBody", make: () => fixtures.makeStatusChange() },
  makeAttempt: { schema: "AttemptBody", make: () => fixtures.makeAttempt() },
  makeAttachment: { schema: "AttachmentBody", make: () => fixtures.makeAttachment() },
  makeComment: { schema: "CommentBody", make: () => fixtures.makeComment() },
  makeProfile: { schema: "ProfileBody", make: () => ({ ...fixtures.makeProfile(), csrf_token: "tok" }) },
  makeInvite: { schema: "InviteBody", make: () => fixtures.makeInvite() },
  makeInvitePreview: { schema: "InvitePreviewResponseBody", make: () => fixtures.makeInvitePreview() },
  makeTenant: { schema: "TenantBody", make: () => fixtures.makeTenant() },
  makeMember: { schema: "MemberBody", make: () => fixtures.makeMember() },
  makeAgentContext: { schema: "AgentContextBody", make: () => fixtures.makeAgentContext() },
  makeLease: { schema: "LeaseBody", make: () => fixtures.makeLease() },
  makeAgentMessage: { schema: "AgentMessageBody", make: () => fixtures.makeAgentMessage() },
  makeAgent: { schema: "AgentBody", make: () => fixtures.makeAgent() },
  makeConductor: { schema: "ConductorBody", make: () => fixtures.makeConductor() },
  makeConductorDirectoryEntry: { schema: "ConductorDirectoryEntryBody", make: () => fixtures.makeConductorDirectoryEntry() },
  makeHarnessConfig: { schema: "GetHarnessConfigResponseBody", make: () => fixtures.makeHarnessConfig() },
  makeHarnessConfigScope: { schema: "ScopeBody", make: () => fixtures.makeHarnessConfigScope() },
  makeVariantVersion: { schema: "VariantVersionBody", make: () => fixtures.makeVariantVersion() },
  makeAgentRole: { schema: "RoleBody", make: () => fixtures.makeAgentRole() },
  makeAgentSession: { schema: "SessionBody", make: () => fixtures.makeAgentSession() },
  makePendingAuthorization: { schema: "PendingAuthResponseBody", make: () => fixtures.makePendingAuthorization() },
  makePendingDeviceAuthorization: {
    schema: "PendingDeviceResponseBody",
    make: () => fixtures.makePendingDeviceAuthorization(),
  },
  makeGrant: { schema: "GrantBody", make: () => fixtures.makeGrant() },
  makeAppWebhook: { schema: "GitHubAppWebhookBody", make: () => fixtures.makeAppWebhook() },
  makeIntakeSource: { schema: "IntakeSourceBody", make: () => fixtures.makeIntakeSource() },
  makeIntakeDelivery: { schema: "IntakeDeliveryBody", make: () => fixtures.makeIntakeDelivery() },
  makeGitHubDelivery: { schema: "DeliveryBody", make: () => fixtures.makeGitHubDelivery() },
  makeGraphNode: { schema: "GraphNode", make: () => fixtures.makeGraphNode() },
  makeGraphEdge: { schema: "GraphEdge", make: () => fixtures.makeGraphEdge() },
  makeBlockingGraph: { schema: "Graph", make: () => fixtures.makeBlockingGraph() },
  makeBlockerCandidate: { schema: "BlockerCandidate", make: () => fixtures.makeBlockerCandidate() },
  makeBlockerList: { schema: "BlockerListResponseBody", make: () => fixtures.makeBlockerList() },
  makeReportState: { schema: "StateTimeBody", make: () => fixtures.makeReportState("open") },
  makeReport: { schema: "ReportBody", make: () => fixtures.makeReport() },
  makeAuthConfig: { schema: "AuthConfigResponseBody", make: () => fixtures.makeAuthConfig() },
  makeUsageSums: { schema: "UsageSumsBody", make: () => fixtures.makeUsageSums() },
  makeLlmUsageReport: { schema: "UsageReportBody", make: () => fixtures.makeLlmUsageReport() },
  makeScopeCostReport: { schema: "ScopeCostBody", make: () => fixtures.makeScopeCostReport() },
  makeLockHistoryReport: { schema: "LockHistoryBody", make: () => fixtures.makeLockHistoryReport() },
  makeShadowDecisionsReport: { schema: "ShadowDecisionsBody", make: () => fixtures.makeShadowDecisionsReport() },
  makeProposal: { schema: "ProposalBody", make: () => fixtures.makeProposal() },
  makeConversation: { schema: "ConversationBody", make: () => fixtures.makeConversation() },
  makeConversationDetail: { schema: "ConversationDetailBody", make: () => fixtures.makeConversationDetail() },
  makeConversationEvent: { schema: "EventBody", make: () => fixtures.makeConversationEvent() },
  makeEditProposal: { schema: "ProposalBody", make: () => fixtures.makeEditProposal() },
  makeSetting: { schema: "SettingBody", make: () => fixtures.makeSetting() },
  makeTenantUserSetting: { schema: "UserSettingBody", make: () => fixtures.makeTenantUserSetting() },
  makeServerSettingChange: { schema: "ServerSettingChangeBody", make: () => fixtures.makeServerSettingChange() },
  makeManifestProposal: { schema: "ProposalBody", make: () => fixtures.makeManifestProposal() },
};

describe("fixture factories", () => {
  it("has a row for every make* factory", () => {
    const exported = Object.keys(fixtures).filter((name) => name.startsWith("make"));
    expect(Object.keys(FACTORIES).sort()).toEqual(exported.sort());
  });

  it.each(Object.entries(FACTORIES))("%s builds a valid schema", (_, { schema, make }) => {
    expect(openApiSchemas.errors(schema, make())).toEqual([]);
  });

  it("names the schema path of a field that has drifted", () => {
    const broken = { ...fixtures.makeComment(), created_at: 7 };
    expect(openApiSchemas.errors("CommentBody", broken)).toEqual([expect.stringContaining("CommentBody/created_at")]);
  });
});
