import { describe, expect, it } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { makeConversationDetail } from "@/test/fixtures";
import { renderSignedIn } from "@/test/render_signed_in";
import RepoState from "./repo_state";

const repo = {
  branch: "ticket-9-x", head: "1234567890abcdef", dirty: true, dirty_count: 2, upstream: "origin/ticket-9-x",
  ahead: 1, behind: 3, commits: [], pushes: [], changed_paths: ["a/b.go", "c.md"], changed_total: 2,
};

describe("RepoState", () => {
  it("shows where the working copy stands, ahead and behind its upstream included, and what the session changed", async () => {
    render(<RepoState detail={makeConversationDetail({ repo })} />);

    expect(screen.getByText(/Working copy at/)).toHaveTextContent(
      "Working copy at 1234567: branch ticket-9-x, 2 uncommitted, 1 ahead, 3 behind origin/ticket-9-x.",
    );
    await userEvent.click(screen.getByText("2 paths changed by this session"));
    expect(screen.getByText("a/b.go")).toBeVisible();
    expect(screen.getByText("c.md")).toBeVisible();
  });

  it("offers no checkout to move: the session never moves the working copy", () => {
    render(<RepoState detail={makeConversationDetail({ repo })} />);

    expect(screen.queryByRole("button")).not.toBeInTheDocument();
    expect(screen.queryByText(/Update|has moved/)).not.toBeInTheDocument();
  });

  it("says how many more paths there were than are listed", () => {
    render(<RepoState detail={makeConversationDetail({ repo: { ...repo, changed_total: 250 } })} />);
    expect(screen.getByText("and 248 more.")).toBeInTheDocument();
  });

  it("says so when the branch has no upstream, or the head is detached", () => {
    render(<RepoState detail={makeConversationDetail({ repo: { ...repo, branch: "", upstream: "", ahead: 3, dirty: false } })} />);
    expect(screen.getByText(/Working copy at/)).toHaveTextContent("detached head, clean, 3 on no remote branch");
  });

  it("warns of a scope an agent holds, naming the ticket", async () => {
    await renderSignedIn(
      <RepoState
        detail={makeConversationDetail({
          repo,
          scope_overlaps: [{ ticket_id: 42, title: "Held work", status: "in_progress", path: "a/b.go", held: "a/" }],
        })}
      />,
    );

    expect(screen.getByRole("alert")).toHaveTextContent("a/b.go is in the scope a/ of #42 (Held work), which is being worked.");
    expect(screen.getByRole("link", { name: "#42" })).toBeInTheDocument();
  });

  it("summarises the overlaps by the agents holding them, and lists a capped number", async () => {
    const overlaps = Array.from({ length: 30 }, (_, i) => ({
      ticket_id: 100 + i, title: `Held ${i}`, status: "in_progress" as const, path: "a/b.go", held: "a/",
    }));
    await renderSignedIn(<RepoState detail={makeConversationDetail({ repo, scope_overlaps: overlaps })} />);

    const alert = screen.getByRole("alert");
    expect(alert).toHaveTextContent("This session changed paths that 30 agents hold");
    expect(within(alert).getAllByRole("listitem")).toHaveLength(20);
    expect(within(alert).getByText("and 10 more.")).toBeInTheDocument();
    expect(alert.querySelector("details")).not.toHaveAttribute("open");
  });

  it("counts an agent once however many of its paths were changed", async () => {
    const overlaps = ["a.go", "b.go", "c.go"].map((path) => ({
      ticket_id: 42, title: "Held work", status: "in_progress" as const, path, held: "./",
    }));
    await renderSignedIn(<RepoState detail={makeConversationDetail({ repo, scope_overlaps: overlaps })} />);

    expect(screen.getByRole("alert")).toHaveTextContent("changed paths that 1 agent holds");
  });

  it("marks a scope inside a shared path as blocking nothing", async () => {
    await renderSignedIn(
      <RepoState
        detail={makeConversationDetail({
          repo,
          scope_overlaps: [
            { ticket_id: 42, title: "Held work", status: "in_progress", path: "db/m/1.sql", held: "db/m/", resolve: "migration" },
          ],
        })}
      />,
    );

    expect(screen.getByRole("alert")).toHaveTextContent("shared path, blocks nothing; on a clash: migration");
  });

  it("warns of nothing when no scope overlaps", () => {
    render(<RepoState detail={makeConversationDetail({ repo, scope_overlaps: [] })} />);
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("shows nothing before the host has reported a repo state", () => {
    render(<RepoState detail={makeConversationDetail()} />);
    expect(screen.queryByText(/Working copy at/)).not.toBeInTheDocument();
  });
});
