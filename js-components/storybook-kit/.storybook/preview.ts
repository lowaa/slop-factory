import type { Preview } from "@storybook/react-vite";
import { assertNoDefaultStyledControls } from "../src/default_style_check";
import { DEFAULT_LOOK_OPTIONS } from "./default_look_options";

declare module "@storybook/react-vite" {
  interface Parameters {
    // Opts a whole story out of the default-look check; the reason is
    // required so the exemption says why the controls are meant to look native.
    defaultStyle?: { skip: string };
  }
}

// The hook an app's preview would register, run on the kit's own stories.
const preview: Preview = {
  parameters: { a11y: { test: "error" } },
  afterEach: ({ canvasElement, parameters }) => {
    assertNoDefaultStyledControls(canvasElement, parameters.defaultStyle?.skip, DEFAULT_LOOK_OPTIONS);
  },
};

export default preview;
