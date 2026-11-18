import { describe, expect, it } from "vitest";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderSignedIn } from "@/test/render_signed_in";
import GuideView from "./guide_view";
import { HelpIndex, HelpPage } from "./help_routes";

const pages = [
  { slug: "one", title: "One", text: "# One\n\n[to two](two.md#part) [here](#top) [web](https://example.com) [bad](../x.md)\n\n## Top\n" },
  { slug: "two", title: "Two", text: "# Two\n" },
];

describe("GuideView", () => {
  it("turns links between guide pages into in-app routes", async () => {
    const { router } = await renderSignedIn(<GuideView page={pages[0]} pages={pages} />);
    expect(screen.getByRole("link", { name: "to two" }).getAttribute("href")).toBe("/help/two#part");
    expect(screen.getByRole("link", { name: "here" }).getAttribute("href")).toBe("/help/one#top");
    await userEvent.click(screen.getByRole("link", { name: "to two" }));
    expect(router.state.location.pathname).toBe("/help/two");
  });

  it("leaves web links alone and does not link anything else", async () => {
    await renderSignedIn(<GuideView page={pages[0]} pages={pages} />);
    expect(screen.getByRole("link", { name: "web" }).getAttribute("href")).toBe("https://example.com");
    expect(screen.queryByRole("link", { name: "bad" })).toBeNull();
  });
});

describe("Help routes", () => {
  it("lists the guide pages", async () => {
    await renderSignedIn(<HelpIndex />);
    expect(screen.getByRole("link", { name: /Running workers/ }).getAttribute("href")).toBe("/help/supervisor");
  });

  it("renders a bundled page", async () => {
    await renderSignedIn(<HelpPage slug="supervisor" />);
    expect(screen.getByRole("heading", { name: /Running workers/ })).toBeTruthy();
  });

  it("says when a page does not exist", async () => {
    await renderSignedIn(<HelpPage slug="nope" />);
    expect(screen.getByText(/no help page called/)).toBeTruthy();
  });
});
