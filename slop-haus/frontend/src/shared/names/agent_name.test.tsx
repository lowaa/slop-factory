import { afterEach, describe, expect, it, vi } from "vitest";
import { act, render, screen } from "@testing-library/react";
import { apiClient, type Agent } from "@/api";
import { makeAgent } from "@/test/fixtures";
import AgentName, { AgentNamesProvider } from "./agent_name";

const ID = "9e3d2eed-5ad6-4ae1-9c1c-29d066e15b4f";

describe("AgentName", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it("shows an agent by name, keeping the id on hover", async () => {
    vi.spyOn(apiClient, "listAgents").mockResolvedValue([makeAgent({ id: ID, name: "builder-1" })]);

    render(
      <AgentNamesProvider>
        <AgentName id={ID} />
      </AgentNamesProvider>,
    );

    const name = await screen.findByText("builder-1");
    expect(name).toHaveAttribute("title", ID);
  });

  // Until the list arrives, or for an agent it doesn't include, a short id
  // still tells agents apart without a whole uuid taking over the line.
  it("falls back to a short id for an agent it cannot name", async () => {
    vi.spyOn(apiClient, "listAgents").mockResolvedValue([]);

    render(
      <AgentNamesProvider>
        <AgentName id={ID} />
      </AgentNamesProvider>,
    );

    expect(await screen.findByText("9e3d2eed")).toHaveAttribute("title", ID);
  });

  it("falls back to a short id outside a provider", () => {
    render(<AgentName id={ID} />);

    expect(screen.getByText("9e3d2eed")).toBeInTheDocument();
  });

  // An agent registered while the board is open gets its name on the next
  // refresh, without a reload.
  it("picks up an agent registered after the page loaded", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    vi.spyOn(apiClient, "listAgents")
      .mockResolvedValueOnce([])
      .mockResolvedValue([makeAgent({ id: ID, name: "builder-9" })]);

    render(
      <AgentNamesProvider>
        <AgentName id={ID} />
      </AgentNamesProvider>,
    );
    expect(await screen.findByText("9e3d2eed")).toBeInTheDocument();

    await act(async () => {
      await vi.advanceTimersByTimeAsync(61_000);
    });

    expect(screen.getByText("builder-9")).toBeInTheDocument();
  });

  // One lookup for the app, not one per name on the page.
  it("shares one lookup across every name under the provider", async () => {
    const other = "11111111-1111-1111-1111-111111111111";
    vi.spyOn(apiClient, "listAgents").mockResolvedValue([
      makeAgent({ id: ID, name: "builder-1" }),
      makeAgent({ id: other, name: "builder-2" }),
    ]);

    render(
      <AgentNamesProvider>
        <AgentName id={ID} />
        <AgentName id={other} />
      </AgentNamesProvider>,
    );

    expect(await screen.findByText("builder-1")).toBeInTheDocument();
    expect(screen.getByText("builder-2")).toBeInTheDocument();
    expect(apiClient.listAgents).toHaveBeenCalledTimes(1);
  });

  it("keeps the names it has when a refresh fails", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    vi.spyOn(apiClient, "listAgents")
      .mockResolvedValueOnce([makeAgent({ id: ID, name: "builder-1" })])
      .mockRejectedValue(new Error("server went away"));

    render(
      <AgentNamesProvider>
        <AgentName id={ID} />
      </AgentNamesProvider>,
    );
    expect(await screen.findByText("builder-1")).toBeInTheDocument();

    await act(async () => {
      await vi.advanceTimersByTimeAsync(61_000);
    });

    expect(screen.getByText("builder-1")).toBeInTheDocument();
  });

  // A slow first load resolving after a newer refresh must not put the
  // older list back.
  it("ignores a list that arrives after a newer one", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    let answerFirst: (agents: Agent[]) => void = () => {};
    vi.spyOn(apiClient, "listAgents")
      .mockImplementationOnce(
        () =>
          new Promise<Agent[]>((resolve) => {
            answerFirst = resolve;
          }),
      )
      .mockResolvedValue([makeAgent({ id: ID, name: "builder-9" })]);

    render(
      <AgentNamesProvider>
        <AgentName id={ID} />
      </AgentNamesProvider>,
    );
    await act(async () => {
      await vi.advanceTimersByTimeAsync(61_000);
    });
    expect(screen.getByText("builder-9")).toBeInTheDocument();

    await act(async () => answerFirst([]));

    expect(screen.getByText("builder-9")).toBeInTheDocument();
  });
});
