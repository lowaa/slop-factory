import type { Meta, StoryObj } from "@storybook/react-vite";
import { useState } from "react";
import { expect, userEvent, waitFor, within } from "storybook/test";
import { ConsentProvider } from "./consent_provider";
import { ConsentStore } from "./consent_store";
import CookiePreferencesButton from "./cookie_preferences_button";
import DoNotSellButton from "./do_not_sell_button";
import ConsentSettings from "./consent_settings";
import { FRESH_VISIT, echoApi, storyPurpose } from "./test/echo_api";
import type { ConsentState } from "./types";

interface StoryArgs {
  initial: ConsentState;
  // The server refuses every decision, which shows the "couldn't be saved" state.
  failSaves?: boolean;
}

function Demo({ initial, failSaves }: StoryArgs) {
  // A store per story, so each starts from its own state.
  const [store] = useState(() => {
    const api = echoApi(initial);
    if (failSaves) {
      api.put = async () => {
        throw new Error("offline");
      };
    }
    return new ConsentStore({ api, locks: null, createChannel: null, gpcSignal: () => false });
  });
  return (
    <ConsentProvider store={store} documentHref={(kind) => `/legal/${kind}`}>
      <main style={{ padding: 24 }}>
        <h1>Page content</h1>
        <p>The app stays usable while the banner is up.</p>
        <ConsentSettings />
        <footer>
          <CookiePreferencesButton />
          <DoNotSellButton />
        </footer>
      </main>
    </ConsentProvider>
  );
}

const meta: Meta<StoryArgs> = {
  title: "privacy/Consent",
  render: (args) => <Demo {...args} />,
  args: { initial: FRESH_VISIT },
};

export default meta;
type Story = StoryObj<StoryArgs>;

export const Banner: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const reject = await canvas.findByRole("button", { name: "Reject all" });
    const accept = canvas.getByRole("button", { name: "Accept all" });

    // As prominent, in the browser's own computed styles rather than the
    // class names: same size, weight, colours and border.
    const look = (el: HTMLElement) => {
      const s = getComputedStyle(el);
      return {
        height: s.height,
        fontSize: s.fontSize,
        fontWeight: s.fontWeight,
        color: s.color,
        background: s.backgroundColor,
        border: s.border,
        padding: s.padding,
      };
    };
    expect(look(reject)).toEqual(look(accept));
    expect(canvas.queryByRole("dialog")).toBeNull();

    await userEvent.click(reject);
    await waitFor(() => expect(canvas.queryByRole("region", { name: "Your privacy choices" })).toBeNull());
  },
};

export const BannerWithGlobalPrivacyControl: Story = {
  args: {
    initial: {
      ...FRESH_VISIT,
      gpcHonoured: true,
      purposes: [storyPurpose("necessary"), storyPurpose("analytics"), storyPurpose("marketing", { gpcLocked: true })],
    },
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const banner = await canvas.findByRole("region", { name: "Your privacy choices" });
    expect(within(banner).getByTestId("gpc-notice")).toBeInTheDocument();
  },
};

export const PreferencesFromTheBanner: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("button", { name: "Manage preferences" }));
    const dialog = await within(document.body).findByRole("dialog", { name: "Cookie preferences" });
    const d = within(dialog);
    expect(d.getByRole("switch", { name: "Strictly necessary" })).toBeDisabled();
    expect(d.getByRole("switch", { name: "Analytics" })).not.toBeChecked();
    await userEvent.click(d.getByRole("switch", { name: "Analytics" }));
    await userEvent.click(d.getByRole("button", { name: "Save preferences" }));
    await waitFor(() => expect(within(document.body).queryByRole("dialog")).toBeNull());
  },
};

export const WithdrawingFromTheFooter: Story = {
  args: {
    initial: {
      ...FRESH_VISIT,
      purposes: [
        storyPurpose("necessary"),
        storyPurpose("analytics", { granted: true, needsDecision: false }),
        storyPurpose("marketing", { needsDecision: false }),
      ],
    },
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    expect(canvas.queryByRole("region", { name: "Your privacy choices" })).toBeNull();
    await userEvent.click(await canvas.findByRole("button", { name: "Cookie preferences" }));
    const dialog = await within(document.body).findByRole("dialog");
    const analytics = within(dialog).getByRole("switch", { name: "Analytics" });
    expect(analytics).toBeChecked();
    await userEvent.click(analytics);
    await userEvent.click(within(dialog).getByRole("button", { name: "Save preferences" }));
    await waitFor(() => expect(within(document.body).queryByRole("dialog")).toBeNull());
  },
};

export const BannerSaveFailed: Story = {
  args: { failSaves: true },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const accept = await canvas.findByRole("button", { name: "Accept all" });
    expect(canvas.queryByRole("alert")).toBeNull();
    await userEvent.click(accept);
    expect(await canvas.findByRole("alert")).toHaveTextContent("Your choice couldn't be saved");
    // The banner stays up: nothing was recorded.
    expect(canvas.getByRole("region", { name: "Your privacy choices" })).toBeInTheDocument();
  },
};

export const PreferencesSaveFailed: Story = {
  args: { failSaves: true },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("button", { name: "Manage preferences" }));
    const dialog = await within(document.body).findByRole("dialog", { name: "Cookie preferences" });
    await userEvent.click(within(dialog).getByRole("switch", { name: "Analytics" }));
    expect(within(dialog).queryByRole("alert")).toBeNull();
    await userEvent.click(within(dialog).getByRole("button", { name: "Save preferences" }));
    expect(await within(dialog).findByRole("alert")).toHaveTextContent("Your choice couldn't be saved");
  },
};
