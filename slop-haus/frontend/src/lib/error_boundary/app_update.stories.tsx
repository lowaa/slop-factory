import type { Meta, StoryObj } from "@storybook/react-vite";
import { AppBuildContext, ChunkLoadFallback, UpdateNotice } from "@slop-factory/logging/error_reporting/react";
import type { ChunkRecovery } from "@slop-factory/logging/error_reporting";
import type { UpdateWatcher } from "@slop-factory/logging/error_reporting";

const meta: Meta = { title: "lib/AppUpdate" };
export default meta;
type Story = StoryObj;

const watcherWith = (tag: string | undefined) =>
  ({ getSnapshot: () => tag, subscribe: () => () => {}, dismiss: () => {} }) as unknown as UpdateWatcher;

export const NewerBuildNotice: Story = {
  render: () => <UpdateNotice watcher={watcherWith("20261027T000000Z-new")} appName="slop-haus" />,
};

// The guard is already set: a reload was tried and didn't help.
export const ReloadMessage: Story = {
  render: () => (
    <AppBuildContext.Provider
      value={{ recovery: { canReload: () => false, attempt: () => "blocked" } as unknown as ChunkRecovery, appName: "slop-haus" }}
    >
      <ChunkLoadFallback error={new TypeError("Failed to fetch dynamically imported module")} />
    </AppBuildContext.Provider>
  ),
};
