import { describe, expect, it } from "vitest";
import { screen } from "@testing-library/react";
import { renderSignedIn } from "@/test/render_signed_in";
import AgentMessages from "./agent_messages";

function renderComposer(heldTicketId: number | null) {
  return renderSignedIn(
    <AgentMessages
      agentId="11111111-1111-1111-1111-111111111111"
      agentName="builder-1"
      messages={[]}
      heldTicketId={heldTicketId}
      composing
      onClose={() => {}}
      onSent={() => {}}
    />,
  );
}

describe("AgentMessages hint", () => {
  it("links to the ticket the agent holds", async () => {
    await renderComposer(42);

    const link = await screen.findByRole("link", { name: "comment on it" });
    expect(link.getAttribute("href")).toContain("/tickets/42");
  });

  it("is plain text when the agent holds nothing", async () => {
    await renderComposer(null);

    expect(await screen.findByText(/comment on it/)).toBeInTheDocument();
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
  });
});
