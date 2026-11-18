import { expect, userEvent, within } from "storybook/test";

// What a phone needs from every Dropdown in a story: a finger-sized trigger,
// and a list that opens inside the screen rather than being cut off.
export async function expectPhoneDropdowns(canvasElement: HTMLElement, count: number) {
  const canvas = within(canvasElement);
  await expect(window.matchMedia("(width <= 720px)").matches).toBe(true);
  const dropdowns = canvas.getAllByRole("combobox").filter((el) => el.tagName === "BUTTON");
  await expect(dropdowns).toHaveLength(count);
  for (const dropdown of dropdowns) {
    await expect(dropdown.getBoundingClientRect().height).toBeGreaterThanOrEqual(44);
  }
  for (const dropdown of dropdowns) {
    dropdown.scrollIntoView({ block: "center" });
    await userEvent.click(dropdown);
    const list = await canvas.findByRole("listbox");
    const rect = list.getBoundingClientRect();
    await expect(rect.top).toBeGreaterThanOrEqual(0);
    await expect(rect.bottom).toBeLessThanOrEqual(window.innerHeight);
    await expect(rect.left).toBeGreaterThanOrEqual(0);
    await expect(rect.right).toBeLessThanOrEqual(window.innerWidth);
    // Not hidden behind, or clipped by, an ancestor.
    const top = document.elementFromPoint(rect.left + rect.width / 2, rect.top + 4);
    await expect(list.contains(top)).toBe(true);
    await userEvent.keyboard("{Escape}");
  }
}
