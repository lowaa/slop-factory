import { describe, expect, it } from "vitest";
import { act, cleanup, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Link } from "@slop-factory/router";
import { renderWithRouter } from "@slop-factory/router/testing";
import HeaderMenu from "./header_menu";

async function renderMenu() {
  const result = await renderWithRouter(
    <>
      <p>Outside the menu</p>
      <HeaderMenu>
        <Link to="/workers">Elsewhere</Link>
        <Link to="/">Here</Link>
        <button type="button">Inside</button>
      </HeaderMenu>
    </>,
  );
  return { ...result, button: screen.getByRole("button", { name: "Menu" }) };
}

// The panel is hidden rather than unmounted, so "closed" is its link being
// out of the accessibility tree rather than out of the document.
function panelOpen(): boolean {
  return screen.queryByRole("link", { name: "Elsewhere" }) !== null;
}

describe("HeaderMenu", () => {
  it("opens and closes from its button", async () => {
    const user = userEvent.setup();
    const { button } = await renderMenu();

    expect(button).toHaveAttribute("aria-expanded", "false");
    expect(panelOpen()).toBe(false);

    await user.click(button);
    expect(button).toHaveAttribute("aria-expanded", "true");
    expect(panelOpen()).toBe(true);

    await user.click(button);
    expect(button).toHaveAttribute("aria-expanded", "false");
    expect(panelOpen()).toBe(false);
  });

  it("stays open for a press inside it", async () => {
    const user = userEvent.setup();
    const { button } = await renderMenu();

    await user.click(button);
    await user.click(screen.getByRole("button", { name: "Inside" }));

    expect(panelOpen()).toBe(true);
  });

  it("closes on a press outside it", async () => {
    const user = userEvent.setup();
    const { button } = await renderMenu();

    await user.click(button);
    await user.click(screen.getByText("Outside the menu"));

    expect(panelOpen()).toBe(false);
  });

  it("closes on Escape, handing focus back to its button", async () => {
    const user = userEvent.setup();
    const { button } = await renderMenu();

    await user.click(button);
    screen.getByRole("button", { name: "Inside" }).focus();
    await user.keyboard("{Escape}");

    expect(panelOpen()).toBe(false);
    expect(button).toHaveFocus();
  });

  it("closes once a link in it navigates", async () => {
    const user = userEvent.setup();
    const { button, router } = await renderMenu();

    await user.click(button);
    await user.click(screen.getByRole("link", { name: "Elsewhere" }));

    expect(router.state.location.pathname).toBe("/workers");
    expect(panelOpen()).toBe(false);
  });

  // No URL changes, so only the link click itself can close it.
  it("closes on a link to the page already on show", async () => {
    const user = userEvent.setup();
    const { button } = await renderMenu();

    await user.click(button);
    await user.click(screen.getByRole("link", { name: "Here" }));

    expect(panelOpen()).toBe(false);
  });

  // Back while the menu is open, say: nothing in the menu was touched.
  it("closes on a navigation from outside it", async () => {
    const user = userEvent.setup();
    const { button, router } = await renderMenu();

    await user.click(button);
    await act(() => router.navigate({ to: "/workers" }));

    expect(panelOpen()).toBe(false);
  });
});

// With a trigger the menu is a dropdown on the desktop header: its button is
// named by the trigger's text rather than "Menu".
describe("HeaderMenu as a dropdown", () => {
  async function renderDropdown(active = false) {
    const result = await renderWithRouter(
      <HeaderMenu trigger="More" active={active}>
        <Link to="/workers">Elsewhere</Link>
        <button type="button">Inside</button>
      </HeaderMenu>,
    );
    return { ...result, button: screen.getByRole("button", { name: "More" }) };
  }

  it("names its button for the trigger, not Menu", async () => {
    await renderDropdown();

    expect(screen.getByRole("button", { name: "More" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Menu" })).not.toBeInTheDocument();
  });

  it("opens and closes from its button", async () => {
    const user = userEvent.setup();
    const { button } = await renderDropdown();

    expect(button).toHaveAttribute("aria-expanded", "false");
    expect(panelOpen()).toBe(false);

    await user.click(button);
    expect(button).toHaveAttribute("aria-expanded", "true");
    expect(panelOpen()).toBe(true);

    await user.click(button);
    expect(button).toHaveAttribute("aria-expanded", "false");
    expect(panelOpen()).toBe(false);
  });

  it("closes on Escape, handing focus back to its button", async () => {
    const user = userEvent.setup();
    const { button } = await renderDropdown();

    await user.click(button);
    screen.getByRole("button", { name: "Inside" }).focus();
    await user.keyboard("{Escape}");

    expect(panelOpen()).toBe(false);
    expect(button).toHaveFocus();
  });

  it("marks its button active only when told the page on show is in it", async () => {
    const { button } = await renderDropdown();
    expect(button).not.toHaveClass("header-menu__button--active");

    cleanup();
    const active = await renderDropdown(true);
    expect(active.button).toHaveClass("header-menu__button--active");
  });

  // Which edge the panel drops from is styling (header_menu.scss); that the
  // account menu's stays on screen is AppShell's DesktopOneRowSeveralTenants
  // story. This is the contract between them.
  it("lines its panel up with the start edge unless told the end", async () => {
    const { button } = await renderDropdown();
    expect(button.parentElement).toHaveClass("header-menu--start");

    cleanup();
    await renderWithRouter(
      <HeaderMenu trigger="Ada" align="end">
        <Link to="/workers">Elsewhere</Link>
      </HeaderMenu>,
    );
    const end = screen.getByRole("button", { name: "Ada" });
    expect(end.parentElement).toHaveClass("header-menu--end");
    expect(end.parentElement).not.toHaveClass("header-menu--start");
  });
});
