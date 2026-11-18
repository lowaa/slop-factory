import { useState, type ReactNode } from "react";
import type { Decorator } from "@storybook/react-vite";
import { createApiGuard } from "@slop-factory/storybook-kit/api_guard";
import { ConsoleLogger } from "@slop-factory/logging/console_logger";
import { TestRouterProvider } from "@slop-factory/router/testing/provider";
import { apiClient, type Profile } from "@/api";
import { AuthContext, type AuthContextValue } from "@/context/auth";
import { LoggerContext } from "@/context/logger";
import { defaultResourceClient } from "@/lib/resource";
import { makeProfile } from "./fixtures";
import { signedInAuth } from "./render_signed_in";
import { WithConsent } from "./with_consent";

// How a component story uses this (preview.ts applies it to every story):
//
//   import { stubApi } from "@/test/story_harness";
//
//   export const WithTenants: Story = {
//     // Signed in as someone else, or at another URL; all optional.
//     parameters: { harness: { profile: makeProfile({ is_admin: true }), initialPath: "/board" } },
//     // Stub each apiClient method the component calls, with what it returns.
//     // They are put back after the story, so none leaks into the next one.
//     beforeEach: () => stubApi({ listTenants: async () => [makeTenant()] }),
//   };
//
// A method a story did not stub throws "apiClient.<name> was called in a
// story without a stub", and the story fails even if the component caught
// the error and drew an error state.

declare module "@storybook/react-vite" {
  interface Parameters {
    harness?: StoryHarnessOptions;
  }
}

export interface StoryHarnessOptions {
  // The signed-in user. Defaults to makeProfile().
  profile?: Profile;
  // Replaces fields of the session, e.g. { status: "unauthenticated", profile: null }.
  auth?: Partial<AuthContextValue>;
  initialPath?: string;
}

// Each story starts with an empty server-state cache: one story's cached
// answers must not stand in for the fetches the next story stubs. The shared
// default client is cleared rather than replaced, as writers invalidate it
// directly.
function FreshResources({ children }: { children: ReactNode }) {
  useState(() => void defaultResourceClient.clear());
  return children;
}

// The router, session, logger and consent the app shell gives a component.
// The logger is console-only: nothing a story logs is exported.
export const withStoryHarness: Decorator = (Story, { parameters }) => {
  const { profile, auth, initialPath } = parameters.harness ?? {};
  const session: AuthContextValue = { ...signedInAuth(profile ?? makeProfile()), ...auth };
  return (
    <TestRouterProvider initialPath={initialPath}>
      <LoggerContext.Provider value={consoleLogger}>
        <AuthContext.Provider value={session}>
          <FreshResources>
            <WithConsent>
              <Story />
            </WithConsent>
          </FreshResources>
        </AuthContext.Provider>
      </LoggerContext.Provider>
    </TestRouterProvider>
  );
};

const consoleLogger = new ConsoleLogger();

const apiGuard = createApiGuard(apiClient, { clientName: "apiClient", stubName: "stubApi" });

// stubApi replaces apiClient methods for the running story.
export const stubApi = apiGuard.stub;

// guardApi is the project-wide beforeEach: every apiClient method fails the
// story unless stubbed; the returned cleanup puts the real ones back.
export const guardApi = apiGuard.guard;

// assertNoUnstubbedApiCalls is the global afterEach.
export const assertNoUnstubbedApiCalls = apiGuard.assertNoUnstubbedCalls;
