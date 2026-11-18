import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect } from "storybook/test";
import preview from "../.storybook/preview";
import { DEFAULT_LOOK_OPTIONS } from "../.storybook/default_look_options";
import {
  CURRENT_THEME,
  NATIVE_LOOK_ATTRIBUTE,
  assertNoDefaultStyledControls,
  findDefaultStyledControls,
  rootAttributeTheme,
  withThemeAsync,
} from "./default_style_check";

// These stories exercise the check itself. The unstyled ones are exempt
// from the preview's afterEach (otherwise they would fail the suite, which is
// the point of the check) and assert the helper's result in play instead.
const meta = {
  title: "Test/DefaultStyleCheck",
  parameters: { layout: "centered" },
} satisfies Meta;

export default meta;
type Story = StoryObj<typeof meta>;

const EXEMPT = { defaultStyle: { skip: "deliberately unstyled: this story tests the check" } };

const STYLED = {
  padding: "8px 10px",
  border: "1px solid rgb(120, 120, 120)",
  background: "rgb(250, 250, 250)",
  color: "rgb(20, 20, 20)",
};

const find = (canvasElement: HTMLElement) => findDefaultStyledControls(canvasElement, DEFAULT_LOOK_OPTIONS);

export const UnstyledControlsAreFound: Story = {
  parameters: EXEMPT,
  render: () => (
    <div>
      <button type="button">Plain</button>
      <select aria-label="Plain select">
        <option>One</option>
      </select>
      <textarea aria-label="Plain textarea" />
      <input type="checkbox" aria-label="Plain checkbox" />
      <input type="radio" aria-label="Plain radio" />
    </div>
  ),
  play: async ({ canvasElement }) => {
    const found = find(canvasElement).map((control) => `${control.element.tagName.toLowerCase()}:${control.theme}`);
    await expect(found).toEqual([
      "button:light",
      "select:light",
      "textarea:light",
      "input:light",
      "input:light",
      "button:dark",
      "select:dark",
      "textarea:dark",
      "input:dark",
      "input:dark",
    ]);
  },
};

export const StyledControlsPass: Story = {
  render: () => (
    <div>
      <button type="button" style={STYLED}>
        Styled
      </button>
      <input type="text" aria-label="Styled field" style={STYLED} />
      <input type="checkbox" aria-label="Styled checkbox" style={{ accentColor: "rgb(200, 0, 100)" }} />
      <input type="radio" aria-label="Styled radio" style={{ accentColor: "rgb(200, 0, 100)" }} />
    </div>
  ),
  play: async ({ canvasElement }) => {
    await expect(find(canvasElement)).toEqual([]);
  },
};

// Styled in the dark theme only, so the light theme still shows the default.
export const StyledInOneThemeOnlyIsFound: Story = {
  parameters: EXEMPT,
  render: () => (
    <>
      <style>
        {
          ':root[data-theme="dark"] .dark-only { padding: 9px 13px; border: 2px solid rgb(10, 100, 10); background: rgb(250, 250, 250); }'
        }
      </style>
      <button type="button" className="dark-only">
        Dark only
      </button>
    </>
  ),
  play: async ({ canvasElement }) => {
    await expect(find(canvasElement).map((control) => control.theme)).toEqual(["light"]);
  },
};

// Without themes the page is checked as it is, which is what an app with a
// single theme wants.
export const NoThemesChecksTheCurrentPage: Story = {
  parameters: EXEMPT,
  render: () => (
    <div>
      <button type="button">Plain</button>
      <button type="button" style={STYLED}>
        Styled
      </button>
    </div>
  ),
  play: async ({ canvasElement }) => {
    const found = findDefaultStyledControls(canvasElement);
    await expect(found.map((control) => [control.element.textContent, control.theme])).toEqual([["Plain", CURRENT_THEME]]);
    await expect(() => assertNoDefaultStyledControls(canvasElement, undefined)).toThrow(/Plain/);
  },
};

export const OptOutNeedsAReason: Story = {
  parameters: EXEMPT,
  render: () => (
    <div>
      <button type="button" {...{ [NATIVE_LOOK_ATTRIBUTE]: "native look wanted in this story" }}>
        Native with reason
      </button>
      <button type="button" {...{ [NATIVE_LOOK_ATTRIBUTE]: "" }}>
        Native without reason
      </button>
    </div>
  ),
  play: async ({ canvasElement }) => {
    const names = [...new Set(find(canvasElement).map((control) => control.element.textContent))];
    await expect(names).toEqual(["Native without reason"]);
  },
};

export const AssertionFailsOnUnexemptedControls: Story = {
  parameters: EXEMPT,
  render: () => <button type="button">Plain</button>,
  play: async ({ canvasElement }) => {
    await expect(() => assertNoDefaultStyledControls(canvasElement, undefined, DEFAULT_LOOK_OPTIONS)).toThrow(
      /default look[\s\S]*Plain" \(light theme\)/,
    );
    await expect(() => assertNoDefaultStyledControls(canvasElement, "native on purpose", DEFAULT_LOOK_OPTIONS)).not.toThrow();
    await expect(() => assertNoDefaultStyledControls(canvasElement, "  ", DEFAULT_LOOK_OPTIONS)).toThrow(/needs a reason/);
  },
};

// The preview's afterEach is what fails a story in the suite; this story
// can't be an unexempted failing story, so it runs the registered hook.
export const PreviewHookRejectsUnstyledControls: Story = {
  parameters: EXEMPT,
  render: () => <button type="button">Plain</button>,
  play: async ({ canvasElement }) => {
    const afterEach = preview.afterEach as (context: { canvasElement: HTMLElement; parameters: Record<string, unknown> }) => void;
    await expect(() => afterEach({ canvasElement, parameters: {} })).toThrow(/default look/);
  },
};

// A rule scoped to a container styles the control; the bare comparison
// element must not pick it up by sitting inside the same container.
export const ContextScopedStylingCounts: Story = {
  render: () => (
    <>
      <style>
        {
          ".scoped-field input { padding: 9px 13px; border: 2px solid rgb(10, 100, 10); background: rgb(250, 250, 250); color: rgb(20, 20, 20); }"
        }
      </style>
      <div className="scoped-field">
        <input type="text" aria-label="Scoped" />
      </div>
    </>
  ),
  play: async ({ canvasElement }) => {
    await expect(find(canvasElement)).toEqual([]);
  },
};

// An element-level rule styles every control of its kind, the bare comparison
// element included unless it is reverted to the browser's own styling: it
// must still count as styled, not match its twin.
export const GloballyStyledControlsPass: Story = {
  render: () => (
    <>
      <style>
        {
          "input, select { padding: 9px 13px; border: 2px solid rgb(10, 100, 10); background: rgb(250, 250, 250); color: rgb(20, 20, 20); }"
        }
      </style>
      <input type="text" aria-label="Bare field" />
      <select aria-label="Bare select">
        <option>One</option>
      </select>
    </>
  ),
  play: async ({ canvasElement }) => {
    await expect(find(canvasElement)).toEqual([]);
  },
};

export const HiddenControlsAreIgnored: Story = {
  parameters: EXEMPT,
  render: () => (
    <div>
      <button type="button" hidden>
        Hidden
      </button>
      <input type="hidden" />
    </div>
  ),
  play: async ({ canvasElement }) => {
    await expect(find(canvasElement)).toEqual([]);
  },
};

export const ThemeIsRestoredAfterTheCheck: Story = {
  render: () => <p>Themed</p>,
  play: async ({ canvasElement }) => {
    const root = document.documentElement;
    root.setAttribute("data-theme", "sepia");
    find(canvasElement);
    await expect(root.getAttribute("data-theme")).toBe("sepia");
    await withThemeAsync(rootAttributeTheme("dark", "data-theme", "dark"), async () => {
      await expect(root.getAttribute("data-theme")).toBe("dark");
    });
    await expect(root.getAttribute("data-theme")).toBe("sepia");
    root.removeAttribute("data-theme");
  },
};
