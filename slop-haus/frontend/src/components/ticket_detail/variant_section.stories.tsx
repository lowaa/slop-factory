import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, userEvent, within } from "storybook/test";
import type { TicketAttempt, VariantOption } from "@/api";
import { makeAttempt as attempt, makeTicket } from "@/test/fixtures";
import { stubApi } from "@/test/story_harness";
import VariantSection from "./variant_section";
import { useVariantOptions, useVariantPicker } from "./use_variant_picker";
import { useTicketStore } from "./use_ticket_store";
import "./ticket_detail.scss";
import "./variant_section.scss";

const TICKET_ID = 7;
const FAST_V2: VariantOption = { id: "v2", name: "fast", version: 2, harness: "pi" };
const FAST_V1: VariantOption = { id: "v1", name: "fast", version: 1, harness: "pi" };

// The picker's state lives in the page that renders it, so the story drives
// the real store and hooks.
function Host() {
  const store = useTicketStore(TICKET_ID);
  const variants = useVariantOptions();
  const picker = useVariantPicker(store, TICKET_ID);
  if (store.state.status !== "loaded") return <p>Loading…</p>;
  return <VariantSection ticket={store.state.ticket} variants={variants} picker={picker} />;
}

const meta = {
  title: "Ticket detail/VariantSection",
  component: Host,
  parameters: { layout: "padded" },
} satisfies Meta<typeof Host>;

export default meta;
type Story = StoryObj<typeof meta>;

function stubTicket(attempts: TicketAttempt[], extra: Parameters<typeof stubApi>[0] = {}) {
  stubApi({
    streamBoardChanges: () => () => {},
    getTicket: async () => makeTicket({ id: TICKET_ID, attempts }),
    listComments: async () => [],
    listAgentContexts: async () => [],
    listTicketAttachments: async () => [],
    listRevisions: async () => [],
    listAttemptReviewRuns: async () => [],
    listStatusHistory: async () => [],
    listVariantOptions: async () => [FAST_V1, FAST_V2],
    ...extra,
  });
}

export const Choosing: Story = {
  beforeEach: () =>
    stubTicket([attempt()], {
      setTicketVariant: async (_id, versionId) =>
        makeTicket({
          id: TICKET_ID,
          attempts: [
            attempt({
              variant: versionId ? { version_id: versionId, name: "fast", version: 2, archived: false } : null,
            }),
          ],
        }),
    }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("combobox"));
    await userEvent.click(await within(document.body).findByRole("option", { name: "fast v2" }));
    await expect(await canvas.findByRole("combobox")).toHaveTextContent("fast v2");
  },
};

export const Locked: Story = {
  beforeEach: () =>
    stubTicket([
      attempt({ variant_settable: false, variant: { version_id: "v2", name: "fast", version: 2, archived: false } }),
    ]),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByText(/Only agents on variant fast v2 claim this/)).toBeVisible();
    await expect(canvas.queryByRole("combobox")).toBeNull();
  },
};
