import type { Preview } from "@storybook/react-vite";
import { AuthPagesProvider, type AuthPagesConfig } from "../src/config";
import { makeTestConfig } from "../src/test/auth_pages_fixture";
import "./preview.scss";
import { assertNoDefaultStyledControls } from "@slop-factory/storybook-kit/default_style_check";

declare module "@storybook/react-vite" {
  interface Parameters {
    // Opts a whole story out of the default-look check; the reason is
    // required so the exemption says why the controls are meant to look native.
    defaultStyle?: { skip: string };
  }
}

// Every story renders inside one AuthPagesProvider, standing in for
// whichever real app's provider would normally wrap these pages -- same
// fake AuthApi (makeTestConfig/makeFakeAuthApi) the component tests use,
// so a story that wants a rejecting signIn (say) doesn't need its own
// hand-rolled fake. A story overrides just the config it cares about via
// `parameters.authConfig` (e.g. `{ registrationOpen: false }`, or a whole
// replacement `api`), the same shape component tests pass to
// TestAuthPagesProvider.
const preview: Preview = {
  parameters: {
    controls: {
      matchers: {
        color: /(background|color)$/i,
        date: /Date$/i,
      },
    },
    // Default is "todo" (a11y violations only warn in the panel); these
    // are shared components every app in this repo consumes, so a
    // violation has to fail `test:storybook` rather than sit unnoticed.
    a11y: {
      test: "error",
    },
  },
  decorators: [
    (Story, context) => {
      const overrides = context.parameters.authConfig as Partial<AuthPagesConfig> | undefined;
      return (
        <AuthPagesProvider config={makeTestConfig(overrides)}>
          <Story />
        </AuthPagesProvider>
      );
    },
  ],
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
