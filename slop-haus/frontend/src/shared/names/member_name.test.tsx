import { afterEach, describe, expect, it, vi } from "vitest";
import { act, render, screen } from "@testing-library/react";
import { apiClient } from "@/api";
import { makeMember } from "@/test/fixtures";
import type { Member } from "@/api";
import MemberName, { MemberNamesProvider } from "./member_name";

const CURRENT_USER_ID = "33333333-3333-3333-3333-333333333333";
const OTHER_ID = "44444444-4444-4444-4444-444444444444";

function mockNames(members: Member[], former: Member[] = []) {
  vi.spyOn(apiClient, "listMemberNames").mockResolvedValue({ members, former });
}

describe("MemberName", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it("shows a member by display name, with no id on hover", async () => {
    mockNames([makeMember({ id: OTHER_ID, display_name: "Grace Hopper" })]);

    render(
      <MemberNamesProvider currentUserId={CURRENT_USER_ID}>
        <MemberName id={OTHER_ID} />
      </MemberNamesProvider>,
    );

    const name = await screen.findByText("Grace Hopper");
    expect(name).not.toHaveAttribute("title");
  });

  it("marks someone who left the tenant as a former member, with no id on hover", async () => {
    mockNames([], [makeMember({ id: OTHER_ID, display_name: "Grace Hopper" })]);

    const { container } = render(
      <MemberNamesProvider currentUserId={CURRENT_USER_ID}>
        <MemberName id={OTHER_ID} />
      </MemberNamesProvider>,
    );

    expect(await screen.findByText("Grace Hopper (former member)")).toBeInTheDocument();
    expect(container.innerHTML).not.toContain(OTHER_ID);
  });

  it("shows 'you' for the signed-in user even if listed as former", async () => {
    mockNames([], [makeMember({ id: CURRENT_USER_ID, display_name: "Ada Lovelace" })]);

    render(
      <MemberNamesProvider currentUserId={CURRENT_USER_ID}>
        <MemberName id={CURRENT_USER_ID} />
      </MemberNamesProvider>,
    );

    expect(await screen.findByText("you")).toBeInTheDocument();
  });

  it("puts no id in any title for a current, former or unknown member", async () => {
    const ids = [OTHER_ID, "55555555-5555-5555-5555-555555555555", "66666666-6666-6666-6666-666666666666"];
    mockNames([makeMember({ id: ids[0], display_name: "A" })], [makeMember({ id: ids[1], display_name: "B" })]);

    const { container } = render(
      <MemberNamesProvider currentUserId={CURRENT_USER_ID}>
        {ids.map((id) => (
          <MemberName key={id} id={id} />
        ))}
      </MemberNamesProvider>,
    );

    await screen.findByText("B (former member)");
    expect(container.querySelectorAll("[title]")).toHaveLength(0);
  });

  // The whole reason CurrentUserIdContext exists: your own comment reads
  // as "you", not your own name reflected back.
  it("shows 'you' for the signed-in user's own id", async () => {
    mockNames([makeMember({ id: CURRENT_USER_ID, display_name: "Ada Lovelace" })]);

    render(
      <MemberNamesProvider currentUserId={CURRENT_USER_ID}>
        <MemberName id={CURRENT_USER_ID} />
      </MemberNamesProvider>,
    );

    expect(await screen.findByText("you")).toBeInTheDocument();
    expect(screen.queryByText("Ada Lovelace")).not.toBeInTheDocument();
  });

  // "someone" rather than a short id -- a member's uuid means nothing to
  // another person reading the board (see member_names.ts).
  it("falls back to 'someone' for a member it cannot name", async () => {
    mockNames([]);

    render(
      <MemberNamesProvider currentUserId={CURRENT_USER_ID}>
        <MemberName id={OTHER_ID} />
      </MemberNamesProvider>,
    );

    expect(await screen.findByText("someone")).toBeInTheDocument();
  });

  it("falls back to 'someone' outside a provider", () => {
    render(<MemberName id={OTHER_ID} />);

    expect(screen.getByText("someone")).toBeInTheDocument();
  });

  // A member added to the tenant while the board is open is named on the
  // next refresh, without a reload -- same contract as AgentNamesProvider.
  it("picks up a member added after the page loaded", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    vi.spyOn(apiClient, "listMemberNames")
      .mockResolvedValueOnce({ members: [], former: [] })
      .mockResolvedValue({ members: [makeMember({ id: OTHER_ID, display_name: "Grace Hopper" })], former: [] });

    render(
      <MemberNamesProvider currentUserId={CURRENT_USER_ID}>
        <MemberName id={OTHER_ID} />
      </MemberNamesProvider>,
    );
    expect(await screen.findByText("someone")).toBeInTheDocument();

    await act(async () => {
      await vi.advanceTimersByTimeAsync(61_000);
    });

    expect(screen.getByText("Grace Hopper")).toBeInTheDocument();
  });

  it("shows 'Deleted user' for a deleted account, with no id to name", () => {
    const { container } = render(
      <MemberNamesProvider currentUserId={CURRENT_USER_ID}>
        <MemberName id={null} deleted />
      </MemberNamesProvider>,
    );

    expect(screen.getByText("Deleted user")).toBeInTheDocument();
    expect(container.querySelectorAll("[title]")).toHaveLength(0);
  });

  it("shows 'Deleted user' over any name when the flag is set", async () => {
    mockNames([makeMember({ id: OTHER_ID, display_name: "Grace Hopper" })]);

    render(
      <MemberNamesProvider currentUserId={CURRENT_USER_ID}>
        <MemberName id={OTHER_ID} deleted />
      </MemberNamesProvider>,
    );

    expect(await screen.findByText("Deleted user")).toBeInTheDocument();
    expect(screen.queryByText("Grace Hopper")).not.toBeInTheDocument();
  });
});
