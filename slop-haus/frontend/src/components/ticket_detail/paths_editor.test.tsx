import { describe, expect, it, vi } from "vitest";
import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient, ApiError } from "@/api";
import { makeComment, makePath, makeTicket } from "@/test/fixtures";
import { TicketDetail, stubTicketDetailDefaults, deferred, load } from "./ticket_detail_test_support";

const EDIT_PATHS = "Edit path scopes";
const BOX = "Path scopes (one per line)";

describe("PathsEditor", () => {
  stubTicketDetailDefaults();

  async function open(base: Parameters<typeof makeTicket>[0]) {
    const user = userEvent.setup();
    load({ id: 5, ...base });
    render(<TicketDetail ticketId={5} />);
    await user.click(await screen.findByRole("button", { name: EDIT_PATHS }));
    return user;
  }

  it("prefills the filed paths only, and saves the edited list with the overlaps the server reports", async () => {
    const overlap = {
      ticket_id: 9,
      title: "Other",
      status: "in_progress" as const,
      path: "app/",
      other: "app/x/",
    };
    const update = vi.spyOn(apiClient, "updateTicket").mockResolvedValue(
      makeTicket({
        id: 5,
        paths: [
          makePath({ path: "app/" }),
          makePath({ path: "docs/" }),
          makePath({ path: "ext/", source: "extended" }),
        ],
        scope_overlaps: [overlap],
      }),
    );
    const user = await open({
      paths: [makePath({ path: "old/" }), makePath({ path: "ext/", source: "extended" })],
    });
    const box = screen.getByLabelText(BOX);
    expect(box).toHaveValue("old/");

    await user.clear(box);
    await user.type(box, "app/{Enter}  docs/ {Enter}");
    await user.click(screen.getByRole("button", { name: "Save" }));

    expect(update).toHaveBeenCalledWith(5, {
      paths: [{ path: "app/" }, { path: "docs/" }],
    });
    expect(await screen.findByText("#9 Other (in_progress): app/ overlaps app/x/")).toBeInTheDocument();
    expect(screen.getByText("docs/")).toBeInTheDocument();
    expect(screen.getByText("ext/")).toBeInTheDocument();
    expect(screen.getByText("(extended)")).toBeInTheDocument();
    expect(screen.queryByLabelText(BOX)).not.toBeInTheDocument();
  });

  it("sends an empty list to clear the scope", async () => {
    const update = vi.spyOn(apiClient, "updateTicket").mockResolvedValue(makeTicket({ id: 5, paths: [] }));
    const user = await open({ paths: [makePath({ path: "old/" })] });

    await user.clear(screen.getByLabelText(BOX));
    await user.click(screen.getByRole("button", { name: "Save" }));

    expect(update).toHaveBeenCalledWith(5, { paths: [] });
    expect(await screen.findByText("No path scopes.")).toBeInTheDocument();
  });

  // The server's own words reach the person: a conflict names the holder.
  it.each([
    ["a scope conflict", 409, 'scope conflict: #5\'s paths would overlap "a/", held by #7'],
    ["paths in two repos", 422, "paths must be in one repo"],
    ["a path that doesn't normalise", 422, 'invalid path "../x"'],
  ])("shows the refusal for %s and keeps the draft and the old scope", async (_name, status, message) => {
    vi.spyOn(apiClient, "updateTicket").mockRejectedValue(new ApiError(status, message));
    const user = await open({ paths: [makePath({ path: "old/" })] });

    await user.type(screen.getByLabelText(BOX), "{Enter}new/");
    await user.click(screen.getByRole("button", { name: "Save" }));

    expect(await screen.findByRole("alert")).toHaveTextContent(message);
    expect(screen.getByLabelText(BOX)).toHaveValue("old/\nnew/");
    expect(screen.getByText("(filed)")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Save" })).toBeEnabled();
  });

  it("shows a busy label and locks the form while saving", async () => {
    const reply = deferred<ReturnType<typeof makeTicket>>();
    vi.spyOn(apiClient, "updateTicket").mockReturnValue(reply.promise);
    const user = await open({ paths: [makePath({ path: "old/" })] });

    await user.click(screen.getByRole("button", { name: "Save" }));

    expect(screen.getByRole("button", { name: "Saving…" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Cancel" })).toBeDisabled();
    expect(screen.getByLabelText(BOX)).toBeDisabled();
    await act(async () => reply.resolve(makeTicket({ id: 5 })));
  });

  it("cancels without sending anything", async () => {
    const update = vi.spyOn(apiClient, "updateTicket");
    const user = await open({ paths: [makePath({ path: "old/" })] });

    await user.click(screen.getByRole("button", { name: "Cancel" }));

    expect(update).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: EDIT_PATHS })).toBeInTheDocument();
  });

  it("does not warn about an agent on a ticket nobody holds", async () => {
    await open({ status: "open", paths: [makePath({ path: "old/" })] });
    expect(screen.queryByText(/An agent holds this ticket/)).not.toBeInTheDocument();
  });

  describe("a held ticket", () => {
    it("says an agent holds it, and shows the comment the server recorded", async () => {
      vi.spyOn(apiClient, "updateTicket").mockResolvedValue(
        makeTicket({
          id: 5,
          status: "in_progress",
          paths: [makePath({ path: "new/" })],
        }),
      );
      const user = await open({
        status: "in_progress",
        paths: [makePath({ path: "old/" })],
      });
      expect(screen.getByRole("status")).toHaveTextContent(
        "An agent holds this ticket. The new scope applies to its claim immediately",
      );

      const listComments = vi.spyOn(apiClient, "listComments").mockResolvedValue([
        makeComment({
          id: 1,
          body: "Path scopes changed by a person. Was: old/. Now: new/.",
        }),
      ]);

      const box = screen.getByLabelText(BOX);
      await user.clear(box);
      await user.type(box, "new/");
      await user.click(screen.getByRole("button", { name: "Save" }));

      await screen.findByText("new/");
      await screen.findByText(/Was: old\/\. Now: new\/\./);
      expect(listComments).toHaveBeenCalled();
    });

    it("does not post a comment itself: the server writes it with the edit", async () => {
      vi.spyOn(apiClient, "updateTicket").mockResolvedValue(
        makeTicket({
          id: 5,
          status: "review",
          paths: [makePath({ path: "new/" })],
        }),
      );
      const addComment = vi.spyOn(apiClient, "addComment");
      const user = await open({
        status: "review",
        paths: [makePath({ path: "old/" })],
      });

      const box = screen.getByLabelText(BOX);
      await user.clear(box);
      await user.type(box, "new/");
      await user.click(screen.getByRole("button", { name: "Save" }));

      await screen.findByText("new/");
      expect(addComment).not.toHaveBeenCalled();
    });
  });
});
