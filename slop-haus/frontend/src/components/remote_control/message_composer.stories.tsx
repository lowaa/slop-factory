import { useState } from "react";
import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, fn, userEvent, within } from "storybook/test";
import { COARSE_POINTER_QUERY } from "@/lib/layout";
import { useAttachmentUploader } from "@/components/attachments";
import MessageComposer, { type StopControl } from "./message_composer";

const ENTER_SENDS_KEY = "remote_control.enter_sends";

// storeEnterSends sets this browser's stored choice for the story (null for
// none) and puts back what was there.
function storeEnterSends(choice: "on" | "off" | null): () => void {
  const before = window.localStorage.getItem(ENTER_SENDS_KEY);
  if (choice === null) window.localStorage.removeItem(ENTER_SENDS_KEY);
  else window.localStorage.setItem(ENTER_SENDS_KEY, choice);
  return () => {
    if (before === null) window.localStorage.removeItem(ENTER_SENDS_KEY);
    else window.localStorage.setItem(ENTER_SENDS_KEY, before);
  };
}

interface HarnessProps {
  initial: string;
  sendDisabled?: boolean;
  sentPrompts?: readonly string[];
  onSubmit: () => void;
  stop?: StopControl;
  attachable?: boolean;
}

// The page owns the text; the composer is controlled.
function Harness({ initial, sendDisabled, sentPrompts, onSubmit, stop, attachable }: HarnessProps) {
  const [value, setValue] = useState(initial);
  const uploader = useAttachmentUploader();
  return (
    <MessageComposer
      value={value}
      onChange={setValue}
      onSubmit={onSubmit}
      sendDisabled={sendDisabled}
      sentPrompts={sentPrompts}
      stop={stop}
      attachments={attachable ? uploader : undefined}
      placeholder="Message the session"
    />
  );
}

const meta = {
  title: "RemoteControl/MessageComposer",
  component: Harness,
  args: { initial: "", onSubmit: fn() },
  beforeEach: () => storeEnterSends(null),
} satisfies Meta<typeof Harness>;

export default meta;
type Story = StoryObj<typeof meta>;

export const Empty: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(canvas.getByRole("textbox", { name: "Message" })).toHaveValue("");
    await expect(canvas.getByRole("button", { name: "Send" })).toBeEnabled();
    await expect(canvas.getByRole("checkbox", { name: "Enter sends" })).toBeChecked();
    await expect(canvas.getByText("Shift+Enter adds a new line")).toBeVisible();
  },
};

// Files are only collected here: uploading them is the conversation view's.
export const WithAttachments: Story = {
  args: { attachable: true },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const file = new File([new Uint8Array(10)], "screenshot.png", { type: "image/png" });
    await userEvent.upload(canvas.getByLabelText("Choose images or videos to attach"), file);
    await expect(canvas.getByText("screenshot.png")).toBeVisible();
    await expect(canvas.getByRole("button", { name: "Remove screenshot.png" })).toBeEnabled();
  },
};

export const WithText: Story = {
  args: { initial: "Run the tests again" },
  play: async ({ canvasElement, args }) => {
    const canvas = within(canvasElement);
    const box = canvas.getByRole("textbox", { name: "Message" });
    await expect(box).toHaveValue("Run the tests again");
    await userEvent.type(box, "{Enter}");
    await expect(args.onSubmit).toHaveBeenCalledTimes(1);
  },
};

export const ShiftEnterAddsNewLine: Story = {
  args: { initial: "first" },
  play: async ({ canvasElement, args }) => {
    const box = within(canvasElement).getByRole("textbox", { name: "Message" });
    await userEvent.type(box, "{Shift>}{Enter}{/Shift}second");
    await expect(box).toHaveValue("first\nsecond");
    await expect(args.onSubmit).not.toHaveBeenCalled();
  },
};

export const Sending: Story = {
  args: { initial: "Run the tests again", sendDisabled: true },
  play: async ({ canvasElement, args }) => {
    const canvas = within(canvasElement);
    await expect(canvas.getByRole("button", { name: "Send" })).toBeDisabled();
    await userEvent.type(canvas.getByRole("textbox", { name: "Message" }), "{Enter}");
    await expect(args.onSubmit).not.toHaveBeenCalled();
  },
};

export const EnterSendsOff: Story = {
  args: { initial: "Run the tests again" },
  beforeEach: () => storeEnterSends("off"),
  play: async ({ canvasElement, args }) => {
    const canvas = within(canvasElement);
    const checkbox = canvas.getByRole("checkbox", { name: "Enter sends" });
    await expect(await canvas.findByText("Enter adds a new line; use Send")).toBeVisible();
    await expect(checkbox).not.toBeChecked();
    const box = canvas.getByRole("textbox", { name: "Message" });
    await userEvent.type(box, "{Enter}");
    await expect(box).toHaveValue("Run the tests again\n");
    await expect(args.onSubmit).not.toHaveBeenCalled();
    await userEvent.click(canvas.getByRole("button", { name: "Send" }));
    await expect(args.onSubmit).toHaveBeenCalledTimes(1);
  },
};

export const TogglingEnterSends: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const checkbox = canvas.getByRole("checkbox", { name: "Enter sends" });
    await userEvent.click(checkbox);
    await expect(checkbox).not.toBeChecked();
    await expect(canvas.getByText("Enter adds a new line; use Send")).toBeVisible();
  },
};

const phone = { viewport: { value: "mobile1", isRotated: false } };

export const PhoneEmpty: Story = { globals: phone };

export const PhoneWithLongText: Story = {
  globals: phone,
  args: { initial: Array.from({ length: 12 }, (_, i) => `Line ${i + 1} of a long message`).join("\n") },
};

// Phone-width alone does not make a touch device: the default follows
// (pointer: coarse), which stories cannot emulate, so the choice is stored.
export const PhoneEnterSendsOff: Story = {
  globals: phone,
  args: { initial: "Run the tests again" },
  beforeEach: () => storeEnterSends("off"),
};

// What a touch device starts with: nothing stored, Enter not sending.
export const TouchDefaultsEnterSendsOff: Story = {
  globals: phone,
  args: { initial: "Run the tests again" },
  beforeEach: () => {
    const query = (q: string) => ({ matches: q === COARSE_POINTER_QUERY, media: q, addEventListener: () => {}, removeEventListener: () => {} });
    const original = window.matchMedia;
    window.matchMedia = query as unknown as typeof window.matchMedia;
    const cleanup = storeEnterSends(null);
    return () => {
      window.matchMedia = original;
      cleanup();
    };
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(canvas.getByRole("checkbox", { name: "Enter sends" })).not.toBeChecked();
    await expect(canvas.getByRole("textbox", { name: "Message" })).toHaveAttribute("enterkeyhint", "enter");
  },
};

const SENT = [
  "hiya this is the 3rd last prompt",
  "and this one is the 2nd last prompt and it's really long long and there's multiple lines but it gets cut off in the list\nsecond line\nthird line",
  "this is the last prompt",
];

export const PromptHistoryOpen: Story = {
  args: { sentPrompts: SENT },
  play: async ({ canvasElement, args }) => {
    const canvas = within(canvasElement);
    const box = canvas.getByRole("textbox", { name: "Message" });
    await userEvent.type(box, "{ArrowUp}");
    const options = within(await canvas.findByRole("listbox")).getAllByRole("option");
    await expect(options).toHaveLength(3);
    await expect(options[2]).toHaveAttribute("aria-selected", "true");
    await expect(box).toHaveAttribute("aria-activedescendant", options[2].id);
    // Above the box, not over it.
    await expect(options[2].getBoundingClientRect().bottom).toBeLessThanOrEqual(box.getBoundingClientRect().top);
    await userEvent.type(box, "{ArrowUp}{Enter}");
    await expect(box).toHaveValue(SENT[1]);
    await expect(args.onSubmit).not.toHaveBeenCalled();
  },
};

export const PhonePromptHistoryOpen: Story = {
  ...PromptHistoryOpen,
  globals: phone,
};

export const Replying: Story = {
  args: { initial: "and then this", stop: { onStop: fn(), stopping: false } },
  play: async ({ canvasElement, args }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByRole("button", { name: "Stop this reply" }));
    await expect(args.stop!.onStop).toHaveBeenCalledTimes(1);
    await userEvent.type(canvas.getByRole("textbox", { name: "Message" }), "{Escape}");
    await expect(args.stop!.onStop).toHaveBeenCalledTimes(2);
    await expect(canvas.getByRole("textbox", { name: "Message" })).toHaveValue("and then this");
  },
};

export const Stopping: Story = {
  args: { stop: { onStop: fn(), stopping: true } },
  play: async ({ canvasElement }) => {
    await expect(within(canvasElement).getByRole("button", { name: "Stopping…" })).toBeDisabled();
  },
};

export const StopUnavailable: Story = {
  args: { stop: { onStop: fn(), stopping: false, unavailable: "The host isn't running" } },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(canvas.getByRole("button", { name: "Stop this reply" })).toBeDisabled();
    await expect(canvas.getByText("The host isn't running")).toBeVisible();
  },
};

const replying: StopControl = { onStop: fn(), stopping: false };

// Stop is above Send on the desktop layout, the same width, and beside it on
// the phone.
async function expectStopLayout(canvasElement: HTMLElement, stacked: boolean) {
  const canvas = within(canvasElement);
  const stopRect = canvas.getByRole("button", { name: "Stop this reply" }).getBoundingClientRect();
  const sendRect = canvas.getByRole("button", { name: "Send" }).getBoundingClientRect();
  if (stacked) {
    await expect(stopRect.bottom).toBeLessThanOrEqual(sendRect.top);
    await expect(stopRect.width).toBeCloseTo(sendRect.width, 0);
    await expect(stopRect.right).toBeCloseTo(sendRect.right, 0);
  } else {
    await expect(stopRect.right).toBeLessThanOrEqual(sendRect.left);
    await expect(stopRect.top).toBeCloseTo(sendRect.top, 0);
  }
}

export const DesktopStopAboveSend: Story = {
  args: { stop: replying },
  play: ({ canvasElement }) => expectStopLayout(canvasElement, true),
};

export const PhoneStopBesideSend: Story = {
  globals: phone,
  args: { stop: replying },
  play: ({ canvasElement }) => expectStopLayout(canvasElement, false),
};

export const DesktopSendOnly: Story = {};

export const PhoneSendOnly: Story = { globals: phone };
