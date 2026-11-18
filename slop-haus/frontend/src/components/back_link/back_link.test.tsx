import { afterEach, describe, expect, it } from "vitest";
import { screen } from "@testing-library/react";
import { renderSignedIn } from "@/test/render_signed_in";
import { tabNamed } from "@/tabs";
import { forgetRememberedFilters, rememberFilterSearch } from "@/components/ticket_filter";
import BackLink from "./back_link";

describe("BackLink", () => {
  afterEach(() => forgetRememberedFilters());

  it("returns to the Unreviewed page and filter last shown", async () => {
    rememberFilterSearch("unreviewed", { q: "login", from: "2026-01-01", after: "c2" });

    await renderSignedIn(<BackLink tab={tabNamed("unreviewed")} />);

    const href = (await screen.findByRole("link", { name: "← Back to unreviewed" })).getAttribute("href");
    expect(href).toBe("/unreviewed?q=login&from=2026-01-01&after=c2");
  });

  it("returns to the filter the board last showed, not the Unreviewed page's", async () => {
    rememberFilterSearch("board", { q: "zebra", done: true });
    rememberFilterSearch("unreviewed", { q: "login" });

    await renderSignedIn(<BackLink tab={tabNamed("board")} />);

    expect(await screen.findByRole("link", { name: "← Back to board" })).toHaveAttribute("href", "/?q=zebra&done=true");
  });

  it("is plain /unreviewed when nothing was filtered", async () => {
    await renderSignedIn(<BackLink tab={tabNamed("unreviewed")} />);

    expect(await screen.findByRole("link", { name: "← Back to unreviewed" })).toHaveAttribute("href", "/unreviewed");
  });

  it("returns to the Blocked page's own filter, which no other page shares", async () => {
    rememberFilterSearch("blocked", { q: "login", exclude: "done" });
    rememberFilterSearch("unreviewed", { q: "other" });

    await renderSignedIn(<BackLink tab={tabNamed("blocked")} />);

    expect(await screen.findByRole("link", { name: "← Back to blocked" })).toHaveAttribute("href", "/blocked?q=login&exclude=done");
  });
});
