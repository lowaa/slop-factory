import { describe, expect, it } from "vitest";
import { screen, within } from "@testing-library/react";
import { makeStatusChange } from "@/test/fixtures";
import { renderSignedIn } from "@/test/render_signed_in";
import StatusHistory from "./status_history";

const AGENT = "aaaaaaaa-1111-1111-1111-111111111111";
const USER = "bbbbbbbb-2222-2222-2222-222222222222";
const PROPOSAL = "cccccccc-3333-3333-3333-333333333333";

function entries() {
  return screen.getAllByRole("listitem");
}

describe("StatusHistory", () => {
  it("renders nothing when there are no changes", async () => {
    const { container } = await renderSignedIn(<StatusHistory changes={[]} />);
    expect(container.querySelector(".status-history")).toBeNull();
  });

  it("lists the changes newest first, from and to", async () => {
    await renderSignedIn(
      <StatusHistory
        changes={[
          makeStatusChange({ id: 1, from_status: null, to_status: "open", actor_kind: "user", actor_id: USER }),
          makeStatusChange({ id: 2, from_status: "open", to_status: "in_progress", actor_id: AGENT }),
        ]}
      />,
    );

    const [newest, oldest] = entries();
    expect(newest).toHaveTextContent("open → in progress");
    expect(within(newest).getByText("aaaaaaaa")).toBeInTheDocument();
    expect(oldest).toHaveTextContent("Created as open");
  });

  it("names the system for an automatic move", async () => {
    await renderSignedIn(
      <StatusHistory
        changes={[
          makeStatusChange({
            actor_kind: "system",
            actor_id: null,
            from_status: "in_progress",
            to_status: "open",
            cause: { kind: "other", text: "lease_expired" },
          }),
        ]}
      />,
    );

    const [entry] = entries();
    expect(within(entry).getByText("System")).toBeInTheDocument();
    expect(within(entry).getByText("lease_expired")).toBeInTheDocument();
  });

  it("does not call a person the route did not name the system", async () => {
    await renderSignedIn(<StatusHistory changes={[makeStatusChange({ actor_kind: "user", actor_id: null })]} />);
    expect(screen.queryByText("System")).toBeNull();
    expect(screen.getByText("A person")).toBeInTheDocument();
  });

  it("says a departed person's account was deleted", async () => {
    await renderSignedIn(
      <StatusHistory changes={[makeStatusChange({ actor_kind: "user", actor_id: null, actor_id_deleted: true })]} />,
    );
    expect(screen.getByText("Deleted user")).toBeInTheDocument();
  });

  it("links a proposal cause to the Approvals tab", async () => {
    await renderSignedIn(
      <StatusHistory
        changes={[makeStatusChange({ actor_kind: "user", actor_id: USER, cause: { kind: "proposal", proposal_id: PROPOSAL } })]}
      />,
    );

    expect(screen.getByRole("link", { name: "proposal" })).toHaveAttribute("href", "/approvals");
    expect(screen.queryByText("auto-approved")).toBeNull();
  });

  it("labels an auto-approved proposal", async () => {
    await renderSignedIn(
      <StatusHistory
        changes={[
          makeStatusChange({
            actor_kind: "user",
            actor_id: USER,
            cause: { kind: "proposal", proposal_id: PROPOSAL, auto_approved: true },
          }),
        ]}
      />,
    );

    expect(screen.getByText("auto-approved")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "proposal" })).toBeInTheDocument();
  });

  it("shows no cause when none was recorded", async () => {
    await renderSignedIn(<StatusHistory changes={[makeStatusChange()]} />);
    expect(entries()[0].querySelector(".status-history__cause")).toBeNull();
  });
});
