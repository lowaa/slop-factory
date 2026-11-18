import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, userEvent, within } from "storybook/test";
import { ApiError } from "@/api";
import { makeProfile, makeTenant } from "@/test/fixtures";
import { stubApi } from "@/test/story_harness";
import NewTenantPage from "./new_tenant_page";

const meta = {
  title: "Tenants/NewTenantPage",
  component: NewTenantPage,
} satisfies Meta<typeof NewTenantPage>;

export default meta;
type Story = StoryObj<typeof meta>;

export const SingleTenantUser: Story = {
  beforeEach: () =>
    stubApi({
      tenantOverview: async () => ({ tenants: [makeTenant({ is_owner: true })], ownerLimit: 3 }),
      createTenant: async () => makeProfile(),
    }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByText(/You own 1 of 3/)).toBeInTheDocument();
    await userEvent.type(canvas.getByLabelText("Tenant name"), "Side project");
    await expect(canvas.getByRole("button", { name: "Create tenant" })).toBeEnabled();
  },
};

export const AtTheLimit: Story = {
  beforeEach: () =>
    stubApi({
      tenantOverview: async () => ({
        tenants: [0, 1, 2].map((i) => makeTenant({ id: `t${i}`, name: `T${i}`, is_owner: true })),
        ownerLimit: 3,
      }),
    }),
  play: async ({ canvasElement }) => {
    await expect(await within(canvasElement).findByRole("status")).toHaveTextContent("You own 3 of 3");
  },
};

export const NameRefused: Story = {
  beforeEach: () =>
    stubApi({
      tenantOverview: async () => ({ tenants: [makeTenant({ is_owner: true })], ownerLimit: 3 }),
      createTenant: async () => {
        throw new ApiError(409, "you already belong to a workspace with that name");
      },
    }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.type(await canvas.findByLabelText("Tenant name"), "Ada's workspace");
    await userEvent.click(canvas.getByRole("button", { name: "Create tenant" }));
    await expect(await canvas.findByRole("alert")).toHaveTextContent("already belong");
  },
};
