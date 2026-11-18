import type { Preview } from "@storybook/react-vite";
import "./preview.scss";
import { assertNoDefaultStyledControls } from "@slop-factory/storybook-kit/default_style_check";

declare module "@storybook/react-vite" {
  interface Parameters {
    // Opts a whole story out of the default-look check; the reason is
    // required so the exemption says why the controls are meant to look native.
    defaultStyle?: { skip: string };
  }
}

const preview: Preview = {
  parameters: {
    layout: "fullscreen",
    // Consent UI is shown to every visitor, so a11y violations fail
    // `test:storybook` instead of sitting in a panel.
    a11y: { test: "error" },
  },
  // afterEach runs once the play function has finished, so controls a play
  // function reveals (opened menus, dialogs) are checked too. Throwing here
  // fails the story under addon-vitest. The preview stylesheet has no
  // data-theme switch (dark follows prefers-color-scheme), so only the
  // page as it is gets checked, not a light/dark pair.
  afterEach: ({ canvasElement, parameters }) => {
    assertNoDefaultStyledControls(canvasElement, parameters.defaultStyle?.skip);
  },
};

export default preview;
