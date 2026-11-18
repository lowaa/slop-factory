import { describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import { apiClient } from "@/api";
import { makeUsageSums } from "@/test/fixtures";
import TicketUsage from "./ticket_usage";

describe("TicketUsage", () => {
  it("shows worker and guidance apart, each per billing mode", async () => {
    vi.spyOn(apiClient, "getTicketLlmUsage").mockResolvedValue({
      worker: [
        makeUsageSums({ billing_mode: "metered", input_tokens: 2000, requests: 3, cost_micros: 1_500_000 }),
        makeUsageSums({ billing_mode: "subscription", input_tokens: 10, cost_micros: 20_000 }),
      ],
      guidance: [makeUsageSums({ billing_mode: "subscription", input_tokens: 500, requests: 2, cost_micros: 40_000 })],
    });
    render(<TicketUsage ticketId={7} />);

    const worker = await screen.findByRole("heading", { name: "Worker" });
    const guidance = screen.getByRole("heading", { name: "Guidance" });
    const workerRows = within(worker.parentElement!).getAllByRole("listitem");
    expect(workerRows).toHaveLength(2);
    expect(workerRows[0]).toHaveTextContent("Metered2k tokens3 requests$1.50 spent");
    expect(workerRows[1]).toHaveTextContent("Subscription10 tokens");
    expect(workerRows[1]).toHaveTextContent("$0.02 estimated");
    const guidanceRows = within(guidance.parentElement!).getAllByRole("listitem");
    expect(guidanceRows).toHaveLength(1);
    expect(guidanceRows[0]).toHaveTextContent("500 tokens2 requests$0.04 estimated");
    expect(apiClient.getTicketLlmUsage).toHaveBeenCalledWith(7);
  });

  it("says unpriced usage is unpriced, not free", async () => {
    vi.spyOn(apiClient, "getTicketLlmUsage").mockResolvedValue({
      worker: [makeUsageSums({ requests: 4, cost_micros: null, unpriced_records: 1, unpriced_requests: 4 })],
      guidance: [],
    });
    render(<TicketUsage ticketId={7} />);
    expect(await screen.findByText("unpriced")).toBeInTheDocument();
    expect(screen.queryByText(/\$0\.00/)).toBeNull();
    expect(screen.queryByRole("heading", { name: "Guidance" })).toBeNull();
  });

  it("shows only the kind there is", async () => {
    vi.spyOn(apiClient, "getTicketLlmUsage").mockResolvedValue({
      worker: [],
      guidance: [makeUsageSums({ input_tokens: 5, cost_micros: 1 })],
    });
    render(<TicketUsage ticketId={7} />);
    expect(await screen.findByRole("heading", { name: "Guidance" })).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Worker" })).toBeNull();
  });

  it("renders nothing when there is no usage, or the read fails", async () => {
    const read = vi.spyOn(apiClient, "getTicketLlmUsage").mockResolvedValue({ worker: [], guidance: [] });
    const { container, rerender } = render(<TicketUsage ticketId={7} />);
    await vi.waitFor(() => expect(read).toHaveBeenCalledTimes(1));
    expect(container).toBeEmptyDOMElement();

    read.mockRejectedValue(new Error("boom"));
    rerender(<TicketUsage ticketId={8} />);
    await vi.waitFor(() => expect(read).toHaveBeenCalledTimes(2));
    expect(container).toBeEmptyDOMElement();
  });
});
