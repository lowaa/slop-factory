import { userEvent, within } from "storybook/test";

// Opens a Dropdown by its accessible name and clicks one of its options.
export async function chooseOption(canvasElement: HTMLElement, label: string, option: string) {
  const canvas = within(canvasElement);
  await userEvent.click(canvas.getByRole("combobox", { name: label }));
  await userEvent.click(await canvas.findByRole("option", { name: option }));
}
