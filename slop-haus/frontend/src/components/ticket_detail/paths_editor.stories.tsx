import { useState } from "react";
import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, fn, userEvent, waitFor, within } from "storybook/test";
import { apiClient, ApiError, type Ticket } from "@/api";
import { makePath, makeTicket } from "@/test/fixtures";
import { stubApi } from "@/test/story_harness";
import PathsEditor from "./paths_editor";
import { usePathsEditor } from "./use_paths_editor";
import type { LoadedState, TicketStore } from "./use_ticket_store";
// The notice, hint and path list rules live in the parent's stylesheet, which
// only TicketDetail imports; without it the story renders them unstyled.
import "./ticket_detail.scss";

const TICKET_ID = 19;
const BOX = "Path scopes (one per line)";

// The real usePathsEditor over a store that holds just the ticket, so the
// stories exercise the component's actual updateTicket call.
function PathsEditorHarness({ initial }: { initial: Ticket }) {
  const [loaded, setLoaded] = useState<LoadedState>({
    status: "loaded",
    ticket: initial,
    comments: [],
    contexts: [],
    revisions: [],
  });
  const store: Pick<TicketStore, "state" | "updateLoaded" | "refreshComments"> = {
    state: loaded,
    updateLoaded: (update) => setLoaded(update),
    refreshComments: async () => {},
  };
  const editor = usePathsEditor(store as TicketStore, TICKET_ID);
  return <PathsEditor ticket={loaded.ticket} editor={editor} />;
}

const PATHS = [
  makePath({ path: "server/internal/ticket/" }),
  makePath({ path: "frontend/src/components/board/" }),
  makePath({ path: "docs/", source: "extended" }),
];

const meta = {
  title: "Ticket detail/Paths editor",
  component: PathsEditorHarness,
  parameters: { layout: "padded" },
  decorators: [
    (Story) => (
      <div className="ticket-detail" style={{ maxWidth: 640 }}>
        <Story />
      </div>
    ),
  ],
} satisfies Meta<typeof PathsEditorHarness>;

export default meta;
type Story = StoryObj<typeof meta>;

export const Viewing: Story = {
  args: { initial: makeTicket({ id: TICKET_ID, paths: PATHS }) },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(canvas.getByText("(extended)")).toBeInTheDocument();
    await expect(canvas.getAllByText("(filed)")).toHaveLength(2);
    await expect(
      canvas.getByRole("button", { name: "Edit path scopes" }),
    ).toBeInTheDocument();
    await expect(canvas.queryByLabelText(BOX)).toBeNull();
  },
};

export const NoPaths: Story = {
  args: { initial: makeTicket({ id: TICKET_ID }) },
  play: async ({ canvasElement }) => {
    await expect(
      within(canvasElement).getByText("No path scopes."),
    ).toBeInTheDocument();
  },
};

export const Editing: Story = {
  args: { initial: makeTicket({ id: TICKET_ID, paths: PATHS }) },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(
      canvas.getByRole("button", { name: "Edit path scopes" }),
    );
    // Only filed paths are editable; the agent's extended one is kept as is.
    await expect(canvas.getByLabelText(BOX)).toHaveValue(
      "server/internal/ticket/\nfrontend/src/components/board/",
    );
    await expect(canvas.getByText(/Paths an agent extended/)).toBeInTheDocument();
    await expect(canvas.getByRole("button", { name: "Save" })).toBeEnabled();
    await expect(canvas.getByRole("button", { name: "Cancel" })).toBeEnabled();
    await expect(canvas.queryByRole("status")).toBeNull();

    await userEvent.click(canvas.getByRole("button", { name: "Cancel" }));
    await expect(canvas.queryByLabelText(BOX)).toBeNull();
  },
};

export const SaveSplitsLines: Story = {
  args: { initial: makeTicket({ id: TICKET_ID, paths: PATHS }) },
  beforeEach: () =>
    stubApi({
      updateTicket: fn(async () =>
        makeTicket({
          id: TICKET_ID,
          paths: [
            makePath({ path: "app/" }),
            makePath({ path: "docs/", source: "extended" }),
          ],
        }),
      ),
    }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(
      canvas.getByRole("button", { name: "Edit path scopes" }),
    );
    const box = canvas.getByLabelText(BOX);
    await userEvent.clear(box);
    await userEvent.type(box, "app/{Enter}  lib/ {Enter}");
    await userEvent.click(canvas.getByRole("button", { name: "Save" }));

    await expect(apiClient.updateTicket).toHaveBeenCalledWith(TICKET_ID, {
      paths: [{ path: "app/" }, { path: "lib/" }],
    });
    await waitFor(() => expect(canvas.queryByLabelText(BOX)).toBeNull());
    await expect(canvas.getByText("app/")).toBeInTheDocument();
  },
};

export const ClearAll: Story = {
  args: { initial: makeTicket({ id: TICKET_ID, paths: [PATHS[0]] }) },
  beforeEach: () =>
    stubApi({
      updateTicket: fn(async () => makeTicket({ id: TICKET_ID, paths: [] })),
    }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(
      canvas.getByRole("button", { name: "Edit path scopes" }),
    );
    await userEvent.clear(canvas.getByLabelText(BOX));
    await userEvent.click(canvas.getByRole("button", { name: "Save" }));

    await expect(apiClient.updateTicket).toHaveBeenCalledWith(TICKET_ID, {
      paths: [],
    });
    await expect(await canvas.findByText("No path scopes.")).toBeInTheDocument();
  },
};

export const HeldTicketNotice: Story = {
  args: {
    initial: makeTicket({ id: TICKET_ID, status: "in_progress", paths: PATHS }),
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(
      canvas.getByRole("button", { name: "Edit path scopes" }),
    );
    await expect(canvas.getByRole("status")).toHaveTextContent(
      "An agent holds this ticket.",
    );
  },
};

export const ScopeConflict: Story = {
  args: { initial: makeTicket({ id: TICKET_ID, paths: PATHS }) },
  beforeEach: () =>
    stubApi({
      updateTicket: fn(async () => {
        throw new ApiError(
          409,
          "path server/internal/ticket/ conflicts with ticket #7, which holds it",
        );
      }),
    }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(
      canvas.getByRole("button", { name: "Edit path scopes" }),
    );
    const box = canvas.getByLabelText(BOX);
    await userEvent.type(box, "{Enter}extra/");
    await userEvent.click(canvas.getByRole("button", { name: "Save" }));

    await expect(await canvas.findByRole("alert")).toHaveTextContent(
      "conflicts with ticket #7",
    );
    // The draft survives the refusal so it can be corrected.
    await expect(box).toHaveValue(
      "server/internal/ticket/\nfrontend/src/components/board/\nextra/",
    );
    await expect(canvas.getByRole("button", { name: "Save" })).toBeEnabled();
  },
};

export const Saving: Story = {
  args: { initial: makeTicket({ id: TICKET_ID, paths: PATHS }) },
  beforeEach: () =>
    stubApi({ updateTicket: fn(() => new Promise<Ticket>(() => {})) }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(
      canvas.getByRole("button", { name: "Edit path scopes" }),
    );
    await userEvent.click(canvas.getByRole("button", { name: "Save" }));
    await expect(apiClient.updateTicket).toHaveBeenCalledWith(TICKET_ID, {
      paths: [
        { path: "server/internal/ticket/" },
        { path: "frontend/src/components/board/" },
      ],
    });
    await expect(
      await canvas.findByRole("button", { name: "Saving…" }),
    ).toBeDisabled();
    await expect(canvas.getByLabelText(BOX)).toBeDisabled();
    await expect(canvas.getByRole("button", { name: "Cancel" })).toBeDisabled();
  },
};
