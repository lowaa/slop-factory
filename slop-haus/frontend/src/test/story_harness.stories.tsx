import { useEffect, useState } from "react";
import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect } from "storybook/test";
import { apiClient } from "@/api";
import { useAuth } from "@/context/auth";
import { useLogger } from "@/context/logger";
import { makeProfile, makeTenant } from "./fixtures";
import { stubApi } from "./story_harness";

// These stories exercise the harness. They run in file order, so
// StubIsGoneAfterwards sees what StubbedCallRenders left behind.
const listTenantsStub = async () => [makeTenant({ name: "Navy" })];

function TenantNames() {
  const { profile } = useAuth();
  // Throws without the harness's logger.
  useLogger();
  const [names, setNames] = useState<string[]>([]);
  useEffect(() => {
    apiClient.listTenants().then((tenants) => setNames(tenants.map((tenant) => tenant.name)));
  }, []);
  return (
    <p>
      {profile?.display_name}: {names.join(", ")}
    </p>
  );
}

const meta = { title: "Test/StoryHarness", component: TenantNames } satisfies Meta<typeof TenantNames>;

export default meta;
type Story = StoryObj<typeof meta>;

export const StubbedCallRenders: Story = {
  parameters: { harness: { profile: makeProfile({ display_name: "Grace Hopper" }) } },
  beforeEach: () => stubApi({ listTenants: listTenantsStub }),
  play: async ({ canvas }) => {
    await expect(await canvas.findByText("Grace Hopper: Navy")).toBeVisible();
  },
};

export const StubIsGoneAfterwards: Story = {
  // Nothing calls the API: this only checks what the previous story left.
  render: () => <p>No call</p>,
  play: async () => {
    await expect(apiClient.listTenants === listTenantsStub).toBe(false);
  },
};
