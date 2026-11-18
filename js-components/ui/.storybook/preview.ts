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

// No provider decorators here, unlike an app's own preview -- see
// main.ts: components in this package must stay portable, so anything they
// needed from a context would be a design smell rather than something to
// wrap the canvas in.
const preview: Preview = {
  parameters: {
    controls: {
      matchers: {
        color: /(background|color)$/i,
        date: /Date$/i,
      },
    },
    // Default is "todo" (a11y violations only warn in the panel). This is
    // the shared component library every app in this repo consumes,
    // so a violation here has to fail `test:storybook` rather than sit
    // unnoticed in a panel nobody's looking at.
    a11y: {
      test: "error",
    },
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
