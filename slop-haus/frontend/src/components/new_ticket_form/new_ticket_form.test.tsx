import { chooseFromDropdown } from "@/test/widgets";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient, ApiError } from "@/api";
import { makeAttachment, makeAgentRole, makeTicket } from "@/test/fixtures";
import NewTicketForm from "./new_ticket_form";

// Most tests are about filing, not the disclosure, so they start with the
// form already opened the way a person would open it.
async function renderExpanded(onCreated: (ticketId: number) => void): Promise<void> {
  render(<NewTicketForm onCreated={onCreated} />);
  await userEvent.click(toggle());
}

function toggle(): HTMLElement {
  return screen.getByRole("button", { name: "File a ticket" });
}

describe("NewTicketForm", () => {
  // No tenant role in most of these tests unless a test says otherwise --
  // the same "there are none" default ticket_detail.test.tsx gives every
  // test for listAgentContexts/listRevisions.
  beforeEach(() => {
    vi.spyOn(apiClient, "listAgentRoles").mockResolvedValue([]);
    vi.spyOn(apiClient, "getOnboardingStatus").mockResolvedValue({ steps: [], code_work_ready: true });
  });

  describe("setup warning", () => {
    it("warns that no worker can do code work while the tenant has no repo and no code host, and still lets a ticket be filed", async () => {
      vi.spyOn(apiClient, "getOnboardingStatus").mockResolvedValue({ steps: [], code_work_ready: false });
      render(<NewTicketForm onCreated={vi.fn()} />);

      expect(await screen.findByRole("note", { hidden: true })).toHaveTextContent("no worker can do code work");
    });

    it("says nothing once the tenant is set up", async () => {
      render(<NewTicketForm onCreated={vi.fn()} />);

      await waitFor(() => expect(apiClient.getOnboardingStatus).toHaveBeenCalled());
      expect(screen.queryByRole("note", { hidden: true })).toBeNull();
    });
  });

  describe("collapsing", () => {
    it("starts collapsed, with the heading still showing", () => {
      render(<NewTicketForm onCreated={vi.fn()} />);

      expect(screen.getByRole("heading", { name: "File a ticket" })).toBeInTheDocument();
      expect(toggle()).toHaveAttribute("aria-expanded", "false");
      expect(screen.queryByRole("textbox", { name: "Title" })).not.toBeInTheDocument();
      expect(screen.queryByRole("button", { name: "Preparing" })).not.toBeInTheDocument();
    });

    it("expands and collapses from the heading's toggle, which controls the fields", async () => {
      const user = userEvent.setup();
      render(<NewTicketForm onCreated={vi.fn()} />);

      await user.click(toggle());
      expect(toggle()).toHaveAttribute("aria-expanded", "true");
      const title = screen.getByRole("textbox", { name: "Title" });
      expect(document.getElementById(toggle().getAttribute("aria-controls")!)).toContainElement(title);

      await user.click(toggle());
      expect(toggle()).toHaveAttribute("aria-expanded", "false");
      expect(screen.queryByRole("textbox", { name: "Title" })).not.toBeInTheDocument();
    });

    it("keeps a half-written draft across collapsing and expanding", async () => {
      const user = userEvent.setup();
      await renderExpanded(vi.fn());

      await user.type(screen.getByRole("textbox", { name: "Title" }), "Draft");
      await user.click(toggle());
      await user.click(toggle());

      expect(screen.getByRole("textbox", { name: "Title" })).toHaveValue("Draft");
    });

    // Someone filing several tickets in a row should not have to reopen
    // the form after each one, whichever button they file with.
    it.each(["Preparing", "Open"])("stays expanded after filing with %s", async (buttonName) => {
      const user = userEvent.setup();
      const onCreated = vi.fn();
      vi.spyOn(apiClient, "createTicket").mockResolvedValue(makeTicket({ id: 7 }));
      await renderExpanded(onCreated);

      await user.type(screen.getByRole("textbox", { name: "Title" }), "One of several");
      await user.click(screen.getByRole("button", { name: buttonName }));

      expect(onCreated).toHaveBeenCalledWith(7);
      expect(toggle()).toHaveAttribute("aria-expanded", "true");
      expect(screen.getByRole("textbox", { name: "Title" })).toHaveValue("");
    });
  });

  it("submits normalized input: trimmed title/body and split, trimmed, non-empty path lines", async () => {
    const user = userEvent.setup();
    vi.spyOn(apiClient, "createTicket").mockResolvedValue(makeTicket({ id: 7 }));
    const onCreated = vi.fn();

    await renderExpanded(onCreated);

    await user.type(screen.getByLabelText("Title"), "  Fix the thing  ");
    await user.type(screen.getByLabelText("Body"), "  Some details  ");
    await chooseFromDropdown(user, "Priority", "high");
    // Leading/trailing blank lines and internal whitespace around real
    // entries -- all of it must be gone from the submitted paths.
    await user.type(
      screen.getByLabelText("Path scopes (one per line)"),
      "\n  server/internal/ticket/  \n\nfrontend/src/components/board/\n",
    );
    await user.click(screen.getByRole("button", { name: "Open" }));

    expect(apiClient.createTicket).toHaveBeenCalledWith({
      title: "Fix the thing",
      body: "Some details",
      priority: "high",
      paths: [{ path: "server/internal/ticket/" }, { path: "frontend/src/components/board/" }],
      needs_discussion: false,
      requires_approval: false,
      status: "open",
    });
    expect(onCreated).toHaveBeenCalledWith(7);
  });

  // The whole point of having two buttons: each files with the status its
  // own label promises, not just whichever one happens to be clicked.
  it.each([
    ["Preparing", "preparing"],
    ["Open", "open"],
  ] as const)("the %s button files the ticket with status %s", async (buttonName, status) => {
    const user = userEvent.setup();
    vi.spyOn(apiClient, "createTicket").mockResolvedValue(makeTicket({ id: 7, status }));

    await renderExpanded(vi.fn());

    await user.type(screen.getByLabelText("Title"), "Some ticket");
    await user.click(screen.getByRole("button", { name: buttonName }));

    expect(apiClient.createTicket).toHaveBeenCalledWith(expect.objectContaining({ status }));
  });

  it("sends needs_discussion: true when the checkbox is checked, and resets it after a successful submit", async () => {
    const user = userEvent.setup();
    vi.spyOn(apiClient, "createTicket").mockResolvedValue(makeTicket({ id: 9 }));

    await renderExpanded(vi.fn());

    await user.type(screen.getByLabelText("Title"), "Needs a plan first");
    await user.click(screen.getByRole("checkbox", { name: "Needs discussion" }));
    await user.click(screen.getByRole("button", { name: "Open" }));

    expect(apiClient.createTicket).toHaveBeenCalledWith(expect.objectContaining({ needs_discussion: true }));
    expect(screen.getByRole("checkbox", { name: "Needs discussion" })).not.toBeChecked();
  });

  it("sends base_ref only when filled in, and shows the server's error for a bad one", async () => {
    const user = userEvent.setup();
    const create = vi
      .spyOn(apiClient, "createTicket")
      .mockRejectedValueOnce(new ApiError(422, "base_ref: no such commit or tag"))
      .mockResolvedValue(makeTicket({ id: 9 }));

    await renderExpanded(vi.fn());

    await user.type(screen.getByLabelText("Title"), "From a tag");
    await user.type(screen.getByLabelText(/Base ref/), " v1.2.0 ");
    await user.click(screen.getByRole("button", { name: "Open" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("no such commit or tag");
    expect(create).toHaveBeenLastCalledWith(expect.objectContaining({ base_ref: "v1.2.0" }));

    await user.click(screen.getByRole("button", { name: "Open" }));
    expect(screen.getByLabelText(/Base ref/)).toHaveValue("");
    await user.type(screen.getByLabelText("Title"), "Plain");
    await user.click(screen.getByRole("button", { name: "Open" }));
    expect(create.mock.lastCall?.[0]).not.toHaveProperty("base_ref");
  });

  it("sends requires_approval: true when the checkbox is checked, and resets it after a successful submit", async () => {
    const user = userEvent.setup();
    vi.spyOn(apiClient, "createTicket").mockResolvedValue(makeTicket({ id: 9 }));

    await renderExpanded(vi.fn());

    await user.type(screen.getByLabelText("Title"), "Wants a human's eyes on the PR");
    await user.click(screen.getByRole("checkbox", { name: "Requires human PR approval" }));
    await user.click(screen.getByRole("button", { name: "Open" }));

    expect(apiClient.createTicket).toHaveBeenCalledWith(expect.objectContaining({ requires_approval: true }));
    expect(screen.getByRole("checkbox", { name: "Requires human PR approval" })).not.toBeChecked();
  });

  it("submits an empty paths array when no scopes are entered", async () => {
    const user = userEvent.setup();
    vi.spyOn(apiClient, "createTicket").mockResolvedValue(makeTicket({ id: 8 }));

    await renderExpanded(vi.fn());

    await user.type(screen.getByLabelText("Title"), "No paths");
    await user.click(screen.getByRole("button", { name: "Open" }));

    expect(apiClient.createTicket).toHaveBeenCalledWith(expect.objectContaining({ title: "No paths", paths: [] }));
  });

  it("shows the server's error message and does not clear the form on failure", async () => {
    const user = userEvent.setup();
    vi.spyOn(apiClient, "createTicket").mockRejectedValue(new ApiError(422, "ticket: title must not be empty"));

    await renderExpanded(vi.fn());

    await user.type(screen.getByLabelText("Title"), "Something");
    await user.click(screen.getByRole("button", { name: "Open" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("ticket: title must not be empty");
    expect(screen.getByLabelText("Title")).toHaveValue("Something");
  });

  // Enter files the ticket, and takes the safe default: Preparing keeps it
  // out of the queue, where Open would hand it straight to an agent.
  it("files as Preparing when submitted with Enter", async () => {
    const user = userEvent.setup();
    vi.spyOn(apiClient, "createTicket").mockResolvedValue(makeTicket({ id: 7, status: "preparing" }));

    await renderExpanded(vi.fn());

    await user.type(screen.getByLabelText("Title"), "Typed then Enter{Enter}");

    expect(apiClient.createTicket).toHaveBeenCalledWith(expect.objectContaining({ status: "preparing" }));
  });

  it("does not file a ticket with no title", async () => {
    const user = userEvent.setup();
    vi.spyOn(apiClient, "createTicket").mockResolvedValue(makeTicket({ id: 7 }));

    await renderExpanded(vi.fn());

    await user.click(screen.getByRole("button", { name: "Open" }));

    expect(apiClient.createTicket).not.toHaveBeenCalled();
  });

  describe("submitting", () => {
    // A slow request must not let a second click file a duplicate ticket
    // through the *other* button either -- both go inert together.
    it("disables both buttons while either submission is in flight", async () => {
      const user = userEvent.setup();
      let resolveCreate: (ticket: ReturnType<typeof makeTicket>) => void = () => {};
      vi.spyOn(apiClient, "createTicket").mockReturnValue(
        new Promise((resolve) => {
          resolveCreate = resolve;
        }),
      );

      await renderExpanded(vi.fn());

      await user.type(screen.getByLabelText("Title"), "Something slow");
      await user.click(screen.getByRole("button", { name: "Preparing" }));

      expect(screen.getByRole("button", { name: "Preparing" })).toBeDisabled();
      expect(screen.getByRole("button", { name: "Open" })).toBeDisabled();
      expect(screen.getByRole("button", { name: "Preparing" })).toHaveAttribute("aria-busy", "true");
      expect(screen.getByRole("button", { name: "Open" })).toHaveAttribute("aria-busy", "false");

      resolveCreate(makeTicket({ id: 1 }));
      await screen.findByRole("button", { name: "Preparing" });
      expect(screen.getByRole("button", { name: "Preparing" })).toBeEnabled();
      expect(screen.getByRole("button", { name: "Open" })).toBeEnabled();
    });

    // The accessible name must not change mid-submission -- a test (or a
    // screen reader user) that asked for "Preparing" should still find it
    // by that name while it's busy, even though the visible label changes.
    it("keeps each button's accessible name stable while it is busy", async () => {
      const user = userEvent.setup();
      vi.spyOn(apiClient, "createTicket").mockReturnValue(new Promise(() => {}));

      await renderExpanded(vi.fn());

      await user.type(screen.getByLabelText("Title"), "Something slow");
      await user.click(screen.getByRole("button", { name: "Preparing" }));

      expect(screen.getByRole("button", { name: "Preparing" })).toBeInTheDocument();
      expect(screen.getByRole("button", { name: "Preparing" })).toHaveTextContent("Filing…");
    });
  });

  describe("role requirement", () => {
    it("shows no picker when the tenant has no roles", async () => {
      await renderExpanded(vi.fn());

      expect(screen.queryByRole("group", { name: "Roles needed to claim this" })).not.toBeInTheDocument();
    });

    // A fetch that failed must not read as "this tenant has no roles" --
    // those look identical as an empty array, and only one of them is
    // actually true.
    it("shows a load-failure hint, not silence, when the roster fetch fails", async () => {
      vi.spyOn(apiClient, "listAgentRoles").mockRejectedValue(new Error("network down"));

      await renderExpanded(vi.fn());

      expect(await screen.findByText("Couldn't load your tenant's roles -- try reloading the page.")).toBeInTheDocument();
    });

    it("offers each of the tenant's roles once loaded", async () => {
      vi.spyOn(apiClient, "listAgentRoles").mockResolvedValue([
        makeAgentRole({ id: "r1", name: "reviewer" }),
        makeAgentRole({ id: "r2", name: "deployer" }),
      ]);

      await renderExpanded(vi.fn());

      const picker = await screen.findByRole("group", { name: "Roles needed to claim this" });
      expect(within(picker).getByRole("checkbox", { name: "reviewer" })).not.toBeChecked();
      expect(within(picker).getByRole("checkbox", { name: "deployer" })).not.toBeChecked();
    });

    it("sends no role_ids when nothing is picked, even with roles on offer", async () => {
      const user = userEvent.setup();
      vi.spyOn(apiClient, "listAgentRoles").mockResolvedValue([makeAgentRole({ id: "r1", name: "reviewer" })]);
      vi.spyOn(apiClient, "createTicket").mockResolvedValue(makeTicket({ id: 7 }));

      await renderExpanded(vi.fn());
      await screen.findByRole("checkbox", { name: "reviewer" });
      await user.type(screen.getByLabelText("Title"), "No requirement");
      await user.click(screen.getByRole("button", { name: "Open" }));

      const call = vi.mocked(apiClient.createTicket).mock.calls[0][0];
      expect(call).not.toHaveProperty("role_ids");
      expect(call).not.toHaveProperty("role_match");
    });

    it("sends the picked role with the default any match, then resets after filing", async () => {
      const user = userEvent.setup();
      vi.spyOn(apiClient, "listAgentRoles").mockResolvedValue([makeAgentRole({ id: "r1", name: "reviewer" })]);
      vi.spyOn(apiClient, "createTicket").mockResolvedValue(makeTicket({ id: 7 }));

      await renderExpanded(vi.fn());
      await user.type(screen.getByLabelText("Title"), "Needs a reviewer");
      await user.click(await screen.findByRole("checkbox", { name: "reviewer" }));
      await user.click(screen.getByRole("button", { name: "Open" }));

      expect(apiClient.createTicket).toHaveBeenCalledWith(
        expect.objectContaining({ role_ids: ["r1"], role_match: "any" }),
      );
      expect(screen.getByRole("checkbox", { name: "reviewer" })).not.toBeChecked();
    });

    it("sends role_match: all when the person picks two roles and chooses 'all of these'", async () => {
      const user = userEvent.setup();
      vi.spyOn(apiClient, "listAgentRoles").mockResolvedValue([
        makeAgentRole({ id: "r1", name: "reviewer" }),
        makeAgentRole({ id: "r2", name: "deployer" }),
      ]);
      vi.spyOn(apiClient, "createTicket").mockResolvedValue(makeTicket({ id: 7 }));

      await renderExpanded(vi.fn());
      await user.type(screen.getByLabelText("Title"), "Needs both");
      await user.click(await screen.findByRole("checkbox", { name: "reviewer" }));
      await user.click(screen.getByRole("checkbox", { name: "deployer" }));
      await user.click(screen.getByRole("radio", { name: "all of these" }));
      await user.click(screen.getByRole("button", { name: "Open" }));

      expect(apiClient.createTicket).toHaveBeenCalledWith(
        expect.objectContaining({ role_ids: ["r1", "r2"], role_match: "all" }),
      );
    });
  });

  describe("tooltips", () => {
    it("shows the Preparing button's explanation on hover, linked by aria-describedby", async () => {
      const user = userEvent.setup();
      await renderExpanded(vi.fn());

      const button = screen.getByRole("button", { name: "Preparing" });
      expect(screen.queryByRole("tooltip")).not.toBeInTheDocument();

      await user.hover(button);

      const tooltip = await screen.findByRole("tooltip");
      expect(tooltip).toHaveTextContent(
        "Saved for you to keep refining -- no agent will pick it up until you open it.",
      );
      expect(button.getAttribute("aria-describedby")).toBe(tooltip.id);
    });

    it("shows the Open button's explanation on keyboard focus", async () => {
      await renderExpanded(vi.fn());

      const button = screen.getByRole("button", { name: "Open" });
      button.focus();

      const tooltip = await screen.findByRole("tooltip");
      expect(tooltip).toHaveTextContent("Open now -- the next free agent may pick it up.");
      expect(button.getAttribute("aria-describedby")).toBe(tooltip.id);
    });
  });

  // Where there is no hover to open the buttons' tooltips, the same words
  // are under the buttons; the stylesheet decides when (layout.spec.ts
  // covers the phone), this that they say the right thing about each.
  it("explains each submit button under the buttons as well as in its tooltip", async () => {
    await renderExpanded(vi.fn());

    const hints = document.querySelector(".new-ticket-form__submit-hints") as HTMLElement;
    const preparing = within(hints).getByText("Preparing:").parentElement!;
    const open = within(hints).getByText("Open:").parentElement!;
    expect(preparing).toHaveTextContent("no agent will pick it up until you open it");
    expect(open).toHaveTextContent("the next free agent may pick it up");
  });
  describe("attachments", () => {
    function pasteScreenshot() {
      const file = new File([new Uint8Array(10)], "image.png", { type: "image/png" });
      fireEvent.paste(screen.getByLabelText("Body"), {
        clipboardData: { items: [{ kind: "file", type: "image/png", getAsFile: () => file }] },
      });
    }

    it("files the ticket first and then uploads a pasted screenshot to it", async () => {
      const user = userEvent.setup();
      const order: string[] = [];
      vi.spyOn(apiClient, "createTicket").mockImplementation(async () => {
        order.push("create");
        return makeTicket({ id: 7 });
      });
      vi.spyOn(apiClient, "uploadTicketAttachment").mockImplementation(async () => {
        order.push("upload");
        return makeAttachment();
      });
      const onCreated = vi.fn();
      await renderExpanded(onCreated);

      await user.type(screen.getByLabelText("Title"), "With a picture");
      pasteScreenshot();
      expect(screen.getByText(/^screenshot-.*\.png$/)).toBeInTheDocument();
      await user.click(screen.getByRole("button", { name: "Preparing" }));

      await waitFor(() => expect(onCreated).toHaveBeenCalledWith(7));
      expect(order).toEqual(["create", "upload"]);
      expect(apiClient.uploadTicketAttachment).toHaveBeenCalledWith(7, expect.any(File), expect.anything());
      expect(screen.queryByText(/^screenshot-/)).toBeNull();
    });

    it("keeps a failed file and retries it against the ticket already filed, not a second one", async () => {
      const user = userEvent.setup();
      vi.spyOn(apiClient, "createTicket").mockResolvedValue(makeTicket({ id: 7 }));
      vi.spyOn(apiClient, "uploadTicketAttachment")
        .mockRejectedValueOnce(new ApiError(413, "the attachment quota is used up"))
        .mockResolvedValueOnce(makeAttachment());
      await renderExpanded(vi.fn());

      await user.type(screen.getByLabelText("Title"), "With a picture");
      pasteScreenshot();
      await user.click(screen.getByRole("button", { name: "Preparing" }));
      expect(await screen.findByText(/Ticket #7 was filed, but some attachments did not upload/)).toBeInTheDocument();

      await user.click(screen.getByRole("button", { name: "Preparing" }));

      await waitFor(() => expect(screen.queryByText(/^screenshot-/)).toBeNull());
      expect(apiClient.createTicket).toHaveBeenCalledTimes(1);
      expect(apiClient.uploadTicketAttachment).toHaveBeenCalledTimes(2);
      expect(vi.mocked(apiClient.uploadTicketAttachment).mock.calls.map(([id]) => id)).toEqual([7, 7]);
    });

    it("files nothing while a file the client turned away is still listed", async () => {
      const user = userEvent.setup();
      vi.spyOn(apiClient, "createTicket").mockResolvedValue(makeTicket({ id: 7 }));
      vi.spyOn(apiClient, "uploadTicketAttachment").mockResolvedValue(makeAttachment());
      await renderExpanded(vi.fn());

      await user.type(screen.getByLabelText("Title"), "With a bad file");
      fireEvent.drop(screen.getByLabelText("Body"), {
        dataTransfer: { types: ["Files"], files: [new File(["<svg/>"], "x.svg", { type: "image/svg+xml" })] },
      });
      await user.click(screen.getByRole("button", { name: "Preparing" }));

      expect(await screen.findByText("Remove the attachments that cannot be sent first.")).toBeInTheDocument();
      expect(apiClient.createTicket).not.toHaveBeenCalled();
    });
  });
});
