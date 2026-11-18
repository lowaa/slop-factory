import { describe, expect, it, vi } from "vitest";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient, type LandingCapabilities } from "@/api";
import { renderSignedIn } from "@/test/render_signed_in";
import MergeMethodRow, {
  mergeQueueUnavailableReason,
} from "./merge_method_row";

const caps = (
  over: Partial<LandingCapabilities> = {},
): LandingCapabilities => ({
  merge_method: "worker",
  merge_queue_enabled: true,
  change_tools: true,
  merge_queue_available: true,
  ...over,
});

describe("MergeMethodRow", () => {
  it("writes the chosen method", async () => {
    vi.spyOn(apiClient, "getLandingCapabilities").mockResolvedValue(caps());
    const onWrite = vi.fn().mockResolvedValue(undefined);
    await renderSignedIn(<MergeMethodRow onWrite={onWrite} />);

    await userEvent.click(
      await screen.findByRole("radio", { name: /server merges from a queue/ }),
    );

    expect(onWrite).toHaveBeenCalledWith("merge_queue");
  });

  it("renders each option as its own row with the description and blocked note inside it", async () => {
    vi.spyOn(apiClient, "getLandingCapabilities").mockResolvedValue(
      caps({ merge_queue_enabled: false, merge_queue_available: false }),
    );
    await renderSignedIn(<MergeMethodRow onWrite={vi.fn()} />);

    const rows = await screen.findAllByTestId("merge-method-choice");
    expect(rows).toHaveLength(2);
    expect(within(rows[0]).getAllByRole("radio")).toHaveLength(1);
    expect(within(rows[1]).getAllByRole("radio")).toHaveLength(1);
    expect(rows[0]).toHaveTextContent("The worker merges");
    expect(rows[1]).toHaveTextContent("The server merges from a queue");
    expect(rows[0]).toHaveTextContent("takes the integration lock");
    expect(rows[1]).toHaveTextContent(
      "workers no longer hold the integration lock",
    );
    expect(within(rows[1]).getByRole("radio")).toHaveAccessibleDescription(
      /workers no longer hold.*switched off/,
    );
    expect(within(rows[0]).queryByRole("note")).toBeNull();
    expect(within(rows[1]).getByRole("note")).toHaveTextContent("switched off");
  });

  it("greys out merge_queue with the reason when the kill switch is off", async () => {
    vi.spyOn(apiClient, "getLandingCapabilities").mockResolvedValue(
      caps({ merge_queue_enabled: false, merge_queue_available: false }),
    );
    await renderSignedIn(<MergeMethodRow onWrite={vi.fn()} />);

    expect(
      await screen.findByRole("radio", { name: /server merges from a queue/ }),
    ).toBeDisabled();
    expect(screen.getByRole("note")).toHaveTextContent("switched off");
  });

  it("greys out merge_queue with the reason when the tenant has no change tools", async () => {
    vi.spyOn(apiClient, "getLandingCapabilities").mockResolvedValue(
      caps({ change_tools: false, merge_queue_available: false }),
    );
    await renderSignedIn(<MergeMethodRow onWrite={vi.fn()} />);

    expect(
      await screen.findByRole("radio", { name: /server merges from a queue/ }),
    ).toBeDisabled();
    expect(screen.getByRole("note")).toHaveTextContent("code host");
  });

  it("gives no reason when the queue is available", () => {
    expect(mergeQueueUnavailableReason(caps())).toBeNull();
  });

  it("lets a tenant already on merge_queue leave it even when it is unavailable", async () => {
    vi.spyOn(apiClient, "getLandingCapabilities").mockResolvedValue(
      caps({
        merge_method: "merge_queue",
        merge_queue_enabled: false,
        merge_queue_available: false,
      }),
    );
    const onWrite = vi.fn().mockResolvedValue(undefined);
    await renderSignedIn(<MergeMethodRow onWrite={onWrite} />);

    await userEvent.click(
      await screen.findByRole("radio", { name: /worker merges/ }),
    );

    expect(onWrite).toHaveBeenCalledWith("worker");
  });
});
