// The tickets half of apiClient's wire behaviour, against a mocked fetch; see index.test.ts
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { allSent, installFetch, jsonResponse, sent, urlsOf, type FetchMock } from "./fetch_recorder";

type Api = typeof import("./index");
type ApiClient = Api["apiClient"];

describe("apiClient tickets", () => {
  let fetchMock: FetchMock;
  let api: Api;
  let apiClient: ApiClient;

  beforeEach(async () => {
    ({ fetchMock, api } = await installFetch());
    apiClient = api.apiClient;
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("carries the feature_disabled code of an attempt endpoint's 404 on the ApiError", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse(404, { status: 404, title: "Not Found", detail: "not found", code: "feature_disabled" }),
    );

    const err = await apiClient.closeTicketAttempt(1, 2).catch((e: unknown) => e);

    expect(err).toMatchObject({ status: 404, message: "not found", code: "feature_disabled" });
  });

  it("unwraps the {tickets: [...]} envelope listTicketsPage gets back, with where the next page starts", async () => {
    fetchMock.mockResolvedValue(jsonResponse(200, { tickets: [{ id: 1, title: "A ticket" }], next_cursor: "c2" }));

    const page = await apiClient.listTicketsPage({});

    expect(page).toEqual({ tickets: [{ id: 1, title: "A ticket" }], nextCursor: "c2" });
    expect(urlsOf(fetchMock)[0]).toBe("/api/tickets");
  });

  it("sends the sort and the page size, and leaves them off when not given", async () => {
    fetchMock.mockImplementation(() => Promise.resolve(jsonResponse(200, { tickets: [] })));

    await apiClient.listTicketsPage({ statuses: ["open"], sort: "priority", limit: 200 });
    await apiClient.listTicketsPage({ statuses: ["open"] });

    expect(urlsOf(fetchMock)[0]).toBe("/api/tickets?status=open&sort=priority&limit=200");
    expect(urlsOf(fetchMock)[1]).toBe("/api/tickets?status=open");
  });

  it("unwraps the {comments: [...]} envelope listComments gets back", async () => {
    fetchMock.mockResolvedValue(jsonResponse(200, { comments: [{ id: 7, body: "looking into it" }] }));

    const comments = await apiClient.listComments(1);

    expect(comments).toEqual([{ id: 7, body: "looking into it" }]);
    expect(urlsOf(fetchMock)[0]).toBe("/api/tickets/1/comments");
  });

  it("unwraps the {contexts: [...]} envelope listAgentContexts gets back", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse(200, { contexts: [{ id: 1, agent_id: "a1", body: "handed off here" }] }),
    );

    const contexts = await apiClient.listAgentContexts(1);

    expect(contexts).toEqual([{ id: 1, agent_id: "a1", body: "handed off here" }]);
    expect(urlsOf(fetchMock)[0]).toBe("/api/tickets/1/contexts");
  });

  it("appends repeated status params, not a comma-separated list", async () => {
    fetchMock.mockResolvedValue(jsonResponse(200, { tickets: [] }));

    await apiClient.listTicketsPage({ statuses: ["open", "blocked"] });

    expect(urlsOf(fetchMock)[0]).toBe("/api/tickets?status=open&status=blocked");
  });

  it("sends the text filter as ?q=, encoded, and leaves a blank one off", async () => {
    fetchMock.mockImplementation(() => Promise.resolve(jsonResponse(200, { tickets: [] })));

    await apiClient.listTicketsPage({ statuses: ["open"], text: "100% board_view&x" });
    await apiClient.listTicketsPage({ statuses: ["open"], text: "   " });

    expect(urlsOf(fetchMock)[0]).toBe("/api/tickets?status=open&q=100%25+board_view%26x");
    expect(urlsOf(fetchMock)[1]).toBe("/api/tickets?status=open");
  });

  it("asks for the tickets awaiting a post-merge review", async () => {
    fetchMock.mockResolvedValue(jsonResponse(200, { tickets: [] }));

    await apiClient.listTicketsPage({ awaitingPostReview: true });

    expect(urlsOf(fetchMock)[0]).toBe("/api/tickets?awaiting_post_review=true");
  });

  it("POSTs a post-merge review, with the comment only when there is one", async () => {
    fetchMock.mockImplementation(() => Promise.resolve(jsonResponse(200, { id: 3, status: "done" })));

    await apiClient.markTicketPostReviewed(3, "Found a leak; filed #9.");
    await apiClient.markTicketPostReviewed(3);

    const first = await sent(fetchMock, 0);
    expect(first.url).toBe("/api/tickets/3/post_review");
    expect(first.method).toBe("POST");
    expect(first.body).toEqual({ comment: "Found a leak; filed #9." });
    expect((await sent(fetchMock, 1)).body).toEqual({});
  });

  it("sends a JSON body on create", async () => {
    fetchMock.mockResolvedValue(jsonResponse(201, { id: 1, title: "New" }));

    await apiClient.createTicket({ title: "New", paths: [{ path: "a/" }] });

    const req = await sent(fetchMock, 0);
    expect(req.method).toBe("POST");
    expect(req.body).toEqual({ title: "New", paths: [{ path: "a/" }] });
  });

  it("sends the status on create when given", async () => {
    fetchMock.mockResolvedValue(jsonResponse(201, { id: 1, title: "New", status: "preparing" }));

    await apiClient.createTicket({ title: "New", status: "preparing" });

    const req = await sent(fetchMock, 0);
    expect(req.body).toEqual({ title: "New", status: "preparing" });
  });

  it("sends role_ids and role_match on create when given", async () => {
    fetchMock.mockResolvedValue(jsonResponse(201, { id: 1, title: "New" }));

    await apiClient.createTicket({ title: "New", role_ids: ["r1", "r2"], role_match: "all" });

    const req = await sent(fetchMock, 0);
    expect(req.body).toEqual({ title: "New", role_ids: ["r1", "r2"], role_match: "all" });
  });

  it("PUTs a ticket's role requirement, defaulting role_match to any", async () => {
    fetchMock.mockResolvedValue(jsonResponse(200, { id: 1, required_roles: [{ id: "r1", name: "reviewer" }] }));

    const ticket = await apiClient.setTicketRoles(1, ["r1"]);

    expect(ticket).toEqual({ id: 1, required_roles: [{ id: "r1", name: "reviewer" }] });
    const req = await sent(fetchMock, 0);
    expect(req.url).toBe("/api/tickets/1/roles");
    expect(req.method).toBe("PUT");
    expect(req.body).toEqual({ role_ids: ["r1"], role_match: "any" });
  });

  it("PUTs a ticket's role requirement with an explicit match mode", async () => {
    fetchMock.mockResolvedValue(jsonResponse(200, { id: 1 }));

    await apiClient.setTicketRoles(1, ["r1", "r2"], "all");

    const req = await sent(fetchMock, 0);
    expect(req.body).toEqual({ role_ids: ["r1", "r2"], role_match: "all" });
  });

  it("POSTs to /ready with no body, returning the updated ticket", async () => {
    fetchMock.mockResolvedValue(jsonResponse(200, { id: 1, status: "open" }));

    const ticket = await apiClient.markTicketReady(1);

    expect(ticket).toEqual({ id: 1, status: "open" });
    const req = await sent(fetchMock, 0);
    expect(req.url).toBe("/api/tickets/1/ready");
    expect(req.method).toBe("POST");
    expect(req.body).toBeUndefined();
  });

  it("PATCHes only the given fields", async () => {
    fetchMock.mockResolvedValue(jsonResponse(200, { id: 1, status: "in_progress" }));

    await apiClient.updateTicket(1, { status: "in_progress" });

    const req = await sent(fetchMock, 0);
    expect(req.url).toBe("/api/tickets/1");
    expect(req.method).toBe("PATCH");
    expect(req.body).toEqual({ status: "in_progress" });
  });

  it("POSTs an edit as a new revision, with the revision it was made against", async () => {
    fetchMock.mockResolvedValue(jsonResponse(200, { id: 1, title: "Renamed", revision: 3 }));

    const ticket = await apiClient.editTicket(1, { title: "Renamed", base_revision: 2 });

    expect(ticket).toEqual({ id: 1, title: "Renamed", revision: 3 });
    const req = await sent(fetchMock, 0);
    expect(req.url).toBe("/api/tickets/1/revisions");
    expect(req.method).toBe("POST");
    expect(req.body).toEqual({ title: "Renamed", base_revision: 2 });
  });

  it("unwraps the revision list", async () => {
    const revision = { id: 5, revision: 1, new_title: "Renamed" };
    fetchMock.mockResolvedValue(jsonResponse(200, { revisions: [revision] }));

    await expect(apiClient.listRevisions(1)).resolves.toEqual([revision]);
    expect(urlsOf(fetchMock)[0]).toBe("/api/tickets/1/revisions");
  });

  it("names no attempt in an approval or a request for changes unless asked to", async () => {
    fetchMock.mockImplementation(async () => jsonResponse(200, { id: 1 }));

    await apiClient.approveTicket(1);
    await apiClient.requestTicketChanges(1, "fix");

    expect((await sent(fetchMock, 0)).url).toBe("/api/tickets/1/approve");
    expect((await sent(fetchMock, 1)).url).toBe("/api/tickets/1/request_changes");
  });

  it("posts a comment body under the 'body' key", async () => {
    fetchMock.mockResolvedValue(jsonResponse(201, { id: 1, body: "hi", author_agent_id: null }));

    await apiClient.addComment(1, "hi");

    const req = await sent(fetchMock, 0);
    expect(req.url).toBe("/api/tickets/1/comments");
    expect(req.body).toEqual({ body: "hi" });
  });

  it("POSTs a comment with needs_discussion, splitting the comment from the ticket it returns", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse(201, { id: 7, body: "plan it", author_user_id: "u1", ticket: { id: 1, needs_discussion: true } }),
    );

    const result = await apiClient.addCommentSettingNeedsDiscussion(1, "plan it", true);

    const req = await sent(fetchMock, 0);
    expect(req.url).toBe("/api/tickets/1/comments");
    expect(req.method).toBe("POST");
    expect(req.body).toEqual({ body: "plan it", needs_discussion: true });
    expect(result).toEqual({
      comment: { id: 7, body: "plan it", author_user_id: "u1" },
      ticket: { id: 1, needs_discussion: true },
    });
  });

  it("sends needs_discussion: false to clear the flag", async () => {
    fetchMock.mockResolvedValue(jsonResponse(201, { id: 8, body: "go", ticket: { id: 1, needs_discussion: false } }));

    await apiClient.addCommentSettingNeedsDiscussion(1, "go", false);

    const req = await sent(fetchMock, 0);
    expect(req.body).toEqual({ body: "go", needs_discussion: false });
  });

  it("POSTs each bulk action's ids and returns the changed tickets", async () => {
    fetchMock.mockImplementation(async () => jsonResponse(200, { tickets: [{ id: 2 }, { id: 1 }] }));

    expect(await apiClient.bulkSetTicketStatus([2, 1], "parked")).toEqual([{ id: 2 }, { id: 1 }]);
    await apiClient.bulkMarkTicketsReady([2, 1]);
    await apiClient.bulkSetTicketPriority([2, 1], "high");

    const calls = await allSent(fetchMock);
    expect(calls.map(({ method, url, body }) => [method, url, body])).toEqual([
      ["POST", "/api/tickets/bulk/status", { ids: [2, 1], status: "parked" }],
      ["POST", "/api/tickets/bulk/ready", { ids: [2, 1] }],
      ["POST", "/api/tickets/bulk/priority", { ids: [2, 1], priority: "high" }],
    ]);
  });

  it("throws a BulkRefusedError listing every refused ticket", async () => {
    const { BulkRefusedError } = api;
    const refusals = [
      { id: 12, status: "in_progress", message: "#12 in progress: only a preparing or open ticket can be parked" },
      { id: 99, status: "", message: "#99: not found" },
    ];
    fetchMock.mockResolvedValue(
      jsonResponse(404, {
        status: 404,
        title: "Not Found",
        detail: "2 ticket(s) refused; nothing was changed.",
        code: "bulk_refused",
        refusals,
      }),
    );

    const err = await apiClient.bulkSetTicketStatus([12, 99], "parked").catch((e: unknown) => e);

    expect(err).toBeInstanceOf(BulkRefusedError);
    expect(err).toMatchObject({ status: 404, code: "bulk_refused", message: "2 ticket(s) refused; nothing was changed." });
    expect((err as InstanceType<typeof BulkRefusedError>).refusals).toEqual(refusals);
  });
});
