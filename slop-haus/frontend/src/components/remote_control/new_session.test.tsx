import type { ReactElement } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render as rtlRender, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient, ApiError } from "@/api";
import { makeConductor, makeConversation } from "@/test/fixtures";
import { expectOneRow } from "./composer_row";
import { UserSettingsProvider } from "@/lib/user_settings_provider";
import NewSession from "./new_session";

const laptop = makeConductor({ id: "c-1", name: "my-laptop" });
const desktop = makeConductor({ id: "c-2", name: "my-desktop" });

function render(ui: ReactElement) {
  return rtlRender(<UserSettingsProvider>{ui}</UserSettingsProvider>);
}

describe("NewSession", () => {
  beforeEach(() => {
    vi.spyOn(apiClient, "startConversation").mockResolvedValue(makeConversation({ id: "s-1" }));
    // A machine with no other session, so a start has nothing to switch from.
    vi.spyOn(apiClient, "listConversations").mockResolvedValue([]);
    vi.spyOn(apiClient, "listMySettings").mockResolvedValue([]);
  });

  it("starts the session with the message, a client id, and hands it on", async () => {
    const onStarted = vi.fn();
    render(<NewSession machines={[laptop]} conductorId="c-1" onPick={vi.fn()} onStarted={onStarted} />);

    await userEvent.type(screen.getByRole("textbox", { name: "Message" }), "  hello  ");
    await userEvent.click(screen.getByRole("button", { name: "Send" }));

    expect(apiClient.startConversation).toHaveBeenCalledWith("c-1", { message: "hello", clientId: expect.stringMatching(/^[0-9a-f-]{36}$/) });
    await waitFor(() => expect(onStarted).toHaveBeenCalledWith(laptop, expect.objectContaining({ id: "s-1" })));
  });

  it("cannot send while no machine is chosen", () => {
    render(<NewSession machines={[laptop]} conductorId="gone" onPick={vi.fn()} onStarted={vi.fn()} />);

    expect(screen.getByRole("button", { name: "Send" })).toBeDisabled();
  });

  it("puts the message box and Send in one row, in that order", () => {
    render(<NewSession machines={[laptop]} conductorId="c-1" onPick={vi.fn()} onStarted={vi.fn()} />);

    expectOneRow(screen.getByRole("textbox", { name: "Message" }), screen.getByRole("button", { name: "Send" }));
  });

  it("fills the message box with a prefilled draft and sends nothing by itself", () => {
    render(<NewSession machines={[laptop]} conductorId="c-1" draft="discuss slop-haus #7" onPick={vi.fn()} onStarted={vi.fn()} />);

    expect(screen.getByRole("textbox", { name: "Message" })).toHaveValue("discuss slop-haus #7");
    expect(apiClient.startConversation).not.toHaveBeenCalled();
  });

  it("can connect with no message", async () => {
    render(<NewSession machines={[laptop]} conductorId="c-1" onPick={vi.fn()} onStarted={vi.fn()} />);

    await userEvent.click(screen.getByRole("button", { name: "Send" }));

    expect(apiClient.startConversation).toHaveBeenCalledWith("c-1", { message: undefined, clientId: expect.any(String) });
  });

  it("offers a choice of machine only when there is one", async () => {
    const onPick = vi.fn();
    const { rerender } = render(<NewSession machines={[laptop]} conductorId="c-1" onPick={onPick} onStarted={vi.fn()} />);
    expect(screen.queryByRole("combobox", { name: "Machine" })).not.toBeInTheDocument();

    rerender(
      <UserSettingsProvider>
        <NewSession machines={[laptop, desktop]} conductorId="c-1" onPick={onPick} onStarted={vi.fn()} />
      </UserSettingsProvider>,
    );
    await userEvent.click(screen.getByRole("combobox", { name: "Machine" }));
    await userEvent.click(screen.getByRole("option", { name: "my-desktop" }));

    expect(onPick).toHaveBeenCalledWith("c-2");
  });

  it("shows the server's refusal and keeps what was typed", async () => {
    vi.spyOn(apiClient, "startConversation").mockRejectedValue(new ApiError(429, "too much is waiting"));
    render(<NewSession machines={[laptop]} conductorId="c-1" onPick={vi.fn()} onStarted={vi.fn()} />);
    await userEvent.type(screen.getByRole("textbox", { name: "Message" }), "hi");

    await userEvent.click(screen.getByRole("button", { name: "Send" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("too much is waiting");
    expect(screen.getByRole("textbox", { name: "Message" })).toHaveValue("hi");
  });

  describe("the draft left in the box", () => {
    const box = () => screen.getByRole("textbox", { name: "Message" });

    it("is cleared once the session has started, and kept when the start fails", async () => {
      const start = vi.spyOn(apiClient, "startConversation").mockRejectedValueOnce(new ApiError(429, "too much is waiting"));
      const first = render(<NewSession machines={[laptop]} conductorId="c-1" onPick={vi.fn()} onStarted={vi.fn()} />);
      await userEvent.type(box(), "keep me");
      await userEvent.click(screen.getByRole("button", { name: "Send" }));
      await screen.findByRole("alert");
      first.unmount();

      const second = render(<NewSession machines={[laptop]} conductorId="c-1" onPick={vi.fn()} onStarted={vi.fn()} />);
      expect(box()).toHaveValue("keep me");
      start.mockResolvedValue(makeConversation({ id: "s-1" }));
      await userEvent.click(screen.getByRole("button", { name: "Send" }));
      await waitFor(() => expect(start).toHaveBeenCalledTimes(2));
      second.unmount();

      render(<NewSession machines={[laptop]} conductorId="c-1" onPick={vi.fn()} onStarted={vi.fn()} />);
      expect(box()).toHaveValue("");
    });

    it("is per machine", async () => {
      const first = render(<NewSession machines={[laptop, desktop]} conductorId="c-1" onPick={vi.fn()} onStarted={vi.fn()} />);
      await userEvent.type(box(), "for the laptop");
      first.unmount();

      render(<NewSession machines={[laptop, desktop]} conductorId="c-2" onPick={vi.fn()} onStarted={vi.fn()} />);
      expect(box()).toHaveValue("");
    });
  });
});
