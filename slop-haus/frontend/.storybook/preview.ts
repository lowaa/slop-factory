import type { Preview } from "@storybook/react-vite";

// The same global tokens the app loads, so a widget looks in Storybook
// exactly as it does in the running app -- a design surface showing
// unstyled controls would be worse than none.
import "../src/index.scss";
import { assertNoDefaultStyledControls } from "@slop-factory/storybook-kit/default_style_check";
import { defaultResourceClient } from "../src/lib/resource";
import { assertNoUnstubbedApiCalls, guardApi, withStoryHarness } from "../src/test/story_harness";

declare module "@storybook/react-vite" {
  interface Parameters {
    // Opts a whole story out of the default-look check; the reason is
    // required so the exemption says why the controls are meant to look native.
    defaultStyle?: { skip: string };
  }
}

const preview: Preview = {
  parameters: {
    backgrounds: { disable: true },
    controls: { matchers: { color: /(background|color)$/i } },
    // Default is "todo" (violations only warn in the panel); failing
    // `test:storybook` instead is what makes the check worth having.
    a11y: { test: "error" },
  },
  decorators: [withStoryHarness],
  // Runs before a story's own beforeEach, so its stubs go on top of the
  // guard; the returned cleanup restores the real client after the story.
  // The server-state cache is module state too, so a story starts and ends
  // with it empty: the previous story's tenants must not answer this one's.
  beforeEach: () => {
    defaultResourceClient.clear();
    const restore = guardApi();
    return () => {
      defaultResourceClient.clear();
      return restore?.();
    };
  },
  // afterEach runs once the play function has finished, so controls a play
  // function reveals (opened menus, dialogs) are checked too. Throwing here
  // fails the story under addon-vitest.
  afterEach: ({ canvasElement, parameters }) => {
    assertNoDefaultStyledControls(canvasElement, parameters.defaultStyle?.skip);
    assertNoUnstubbedApiCalls();
  },
};

export default preview;
