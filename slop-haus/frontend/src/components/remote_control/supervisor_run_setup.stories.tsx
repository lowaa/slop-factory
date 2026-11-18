import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, fn, userEvent, within } from "storybook/test";
import { apiClient, type DesiredAgentView } from "@/api";
import { stubApi } from "@/test/story_harness";
import SupervisorRunSetup from "./supervisor_run_setup";

const box = { id: "s-1", name: "fake-box", remote_controls: [] as DesiredAgentView[] };

function entry(name: string, observed: DesiredAgentView["observed"]): DesiredAgentView {
  return { id: `e-${name}`, kind: "remote_control", name, harness: "claude", state: "running", launch: "supervisor", generation: 1, resume_generation: 0, observed };
}

const meta = {
  title: "RemoteControl/SupervisorRunSetup",
  component: SupervisorRunSetup,
  parameters: { layout: "padded" },
  args: { run: async (action: () => Promise<void>) => action(), onAdded: fn() },
} satisfies Meta<typeof SupervisorRunSetup>;

export default meta;
type Story = StoryObj<typeof meta>;

export const OneMachineWithProgress: Story = {
  args: {
    own: {
      provisionFromBoard: true,
      supervisors: [
        {
          ...box,
          remote_controls: [
            entry("fake-starting", { generation: 1, phase: "provisioning" }),
            entry("fake-broken", { generation: 1, phase: "failed", reason: "fatal: could not read Username" }),
          ],
        },
      ],
    },
  },
  beforeEach: () => stubApi({ addOwnRemoteControl: fn(async () => entry("fake-new", { generation: 0 })) }),
  play: async ({ canvasElement, args }) => {
    const canvas = within(canvasElement);
    const form = canvas.getByRole("form", { name: "Add a remote control on a machine" });
    await expect(form).toHaveTextContent("On fake-box");
    await expect(canvas.getByRole("list", { name: "Remote controls your supervisors run" })).toHaveTextContent(
      "fake-broken on fake-box: Failed: fatal: could not read Username",
    );
    await userEvent.type(within(form).getByLabelText("Remote control name"), "fake-new");
    await userEvent.click(within(form).getByRole("button", { name: "Add remote control" }));
    await expect(apiClient.addOwnRemoteControl).toHaveBeenCalledWith("s-1", "fake-new", undefined);
    await expect(args.onAdded).toHaveBeenCalledTimes(1);
  },
};

export const TwoMachines: Story = {
  args: { own: { provisionFromBoard: true, supervisors: [box, { ...box, id: "s-2", name: "fake-other-box" }] } },
  play: async ({ canvasElement }) => {
    await expect(within(canvasElement).getByLabelText("Machine")).toBeVisible();
  },
};

export const NoMachineOfYours: Story = {
  args: { own: { provisionFromBoard: true, supervisors: [] } },
  play: async ({ canvasElement }) => {
    await expect(within(canvasElement).getByRole("link", { name: "the Workers page" })).toBeVisible();
  },
};

export const BoardNotProvisioning: Story = {
  args: { own: { provisionFromBoard: false, supervisors: [] } },
  play: async ({ canvasElement }) => {
    await expect(within(canvasElement).getByRole("note")).toHaveTextContent("does not provision agents from the board");
  },
};
